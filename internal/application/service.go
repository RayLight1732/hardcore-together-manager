// Package application implements ChallengeApplicationService, the single
// place every use case (/start [clean], /load, /deactivate, archive-request,
// ready/running-changed/disconnect, /savedata, /senpan) is carried out
// (architecture-manager.md 8節・8a節, spec 7.3節・7.4節). It depends only on
// internal/port and internal/domain/*, never on a concrete adapter —
// adapter/modserver and adapter/gateserver are thin protocol translators
// that call into this service, and this service calls back into them only
// through the port.GateNotifier / port.ReadyWaiter interfaces they happen
// to implement.
package application

import (
	"context"
	"fmt"
	"log"
	"sync"
	"time"

	"github.com/RayLight1732/hardcore-together-manager/internal/domain/challenge"
	"github.com/RayLight1732/hardcore-together-manager/internal/domain/records"
	"github.com/RayLight1732/hardcore-together-manager/internal/port"
)

// Deps are the ports ChallengeApplicationService is built from.
type Deps struct {
	State   port.ChallengeStateRepository
	Process port.ProcessRunner
	World   port.WorldPreparer
	Archive port.ArchiveRepository
	Records port.RecordsRepository
	Gate    port.GateNotifier
	Ready   port.ReadyWaiter
	Clock   port.Clock
}

// Timeouts bounds the blocking waits inside Start/Load/Deactivate (14節の
// 未確定事項: specific values are still open, see config.yml's timeouts
// section).
type Timeouts struct {
	Evacuate    time.Duration
	Ready       time.Duration
	ProcessStop time.Duration
}

// ChallengeApplicationService is the use-case layer tying every port
// together. opMutex is fully internal now (unlike the pre-layering
// orchestrator, which had to share it with modserver directly) because
// archive-request now always goes through HandleArchiveRequest instead of
// adapters touching ArchiveRepository themselves.
type ChallengeApplicationService struct {
	deps     Deps
	timeouts Timeouts
	opMutex  sync.Mutex
}

// New builds a ChallengeApplicationService.
func New(deps Deps, timeouts Timeouts) *ChallengeApplicationService {
	return &ChallengeApplicationService{deps: deps, timeouts: timeouts}
}

// Snapshot returns the current {phase, running}, for state-query. requestID
// is unused here — Snapshot never calls back into port.GateNotifier — but is
// accepted for uniformity with every other Application method
// (docs/protocol-gate-manager.md 1節, architecture-manager.md 7節).
func (s *ChallengeApplicationService) Snapshot(requestID string) challenge.State {
	return s.deps.State.Snapshot()
}

// SaveData returns every save/death/clear event across all challenges,
// oldest first, for /savedata. See Snapshot's doc comment re: requestID.
func (s *ChallengeApplicationService) SaveData(requestID string) ([]records.SaveDataEntry, error) {
	all, err := s.deps.Records.ReadAll()
	if err != nil {
		return nil, err
	}
	return records.AggregateSaveData(all), nil
}

// Senpan tallies deaths by player across all challenges, for /senpan
// list|count (mode only affects Gate's display, per docs/protocol-gate-manager.md 3.7節).
// See Snapshot's doc comment re: requestID.
func (s *ChallengeApplicationService) Senpan(requestID string) ([]records.SenpanEntry, error) {
	all, err := s.deps.Records.ReadAll()
	if err != nil {
		return nil, err
	}
	return records.AggregateSenpan(all), nil
}

// HandleReady is called when the hardcore MOD sends `ready`
// (docs/protocol-mod-manager.md 3.1節). The adapter that received it
// (adapter/modserver) is also responsible for unblocking its own
// port.ReadyWaiter implementation — that's a separate, adapter-internal
// concern from the state mutation here.
func (s *ChallengeApplicationService) HandleReady(running bool) {
	s.deps.State.MarkReady(running)
}

// HandleRunningChanged is called when the hardcore MOD sends
// `running-changed` (docs/protocol-mod-manager.md 3.2節).
func (s *ChallengeApplicationService) HandleRunningChanged(running bool) {
	s.deps.State.SetRunning(running)
}

// HandleDisconnect is called when the MOD⇔Manager connection is lost
// (docs/protocol-mod-manager.md 5節).
//
// While a Manager-initiated stop is already in flight (phase is starting —
// mid Start clean/Load's own evacuate+Process.Stop — or stopping — mid
// Deactivate), a disconnect is expected, not a signal of anything: that
// operation's own state.MarkStopped/MarkDeactivated call will record the
// authoritative outcome once it completes. Treating it as a "process died"
// signal here would race that call — the MOD's TCP socket closing (the
// child's own doing, moments before it exits) and Process.IsRunning()
// flipping to false (only once Manager's os/exec Wait() reaps it) are two
// independent OS notifications with no guaranteed order.
//
// Only while phase is ready (nothing asked the process to stop) does a
// disconnect indicate something needs distinguishing: if the process
// itself is still alive, it's the safe-side "just the TCP link dropped"
// case (running -> unknown); if the process has died on its own (e.g. the
// JVM crashed), there's nothing left that could send a late
// running-changed to correct an unknown guess, so the last persisted value
// (adapter/fsstate) is kept as-is instead.
func (s *ChallengeApplicationService) HandleDisconnect() {
	if s.deps.State.Snapshot().Phase != challenge.PhaseReady {
		return
	}
	if s.deps.Process.IsRunning() {
		s.deps.State.MarkUnknown()
	}
}

// HandleArchiveRequest carries out spec 3.2節's archive-request handling:
// resolve/generate the name, copy world/, write metadata. It shares
// opMutex with Start/Load/Deactivate so a challenge restart/stop and an
// in-flight archive copy never touch world/ at the same time.
func (s *ChallengeApplicationService) HandleArchiveRequest(name string, elapsedTime int64) (finalName string, err error) {
	s.opMutex.Lock()
	defer s.opMutex.Unlock()

	return s.deps.Archive.Save(name, elapsedTime, s.deps.Clock.Now())
}

// Start implements /start [clean] (spec 2.1節・7.3節・7.4節). clean:true is
// the destructive "wipe and regenerate" path (Start's original behavior,
// architecture-manager.md 8節); clean:false is the resume-only path that
// never touches world/ and never looks at running, the structural fix for
// the initial /start deadlock (architecture-manager.md 8a節). requestID is
// the UUID Gate attached to this request (docs/protocol-gate-manager.md
// 1節); it is threaded through unchanged to whatever terminal message
// (start-rejected/start-failed/hardcore-ready) eventually answers it.
func (s *ChallengeApplicationService) Start(ctx context.Context, requestID string, clean bool, requestedBy string) error {
	if clean {
		return s.startClean(ctx, requestID, requestedBy)
	}
	return s.startResume(ctx, requestID, requestedBy)
}

// startClean is /start clean (architecture-manager.md 8節). Its running
// check is always skipped (clean means "I accept losing whatever challenge
// is in progress") — the only thing that can still reject it is a
// mid-transition phase (challenge.DecideStart's guard).
//
// architecture-manager.md 8節's original pseudocode acquired opMutex before
// the running check; doing so lets a concurrent /start block for the
// entire in-progress sequence before learning it will just be rejected,
// contradicting the claim that a second /start during "starting" is turned
// away immediately. This checks+commits the running state first
// (state.TryMarkStarting is atomic on its own, no lock needed) and only
// takes opMutex afterward, right before the file/process operations it
// actually needs to serialize with archive-request.
func (s *ChallengeApplicationService) startClean(ctx context.Context, requestID, requestedBy string) error {
	log.Printf("application: start clean requested by %s (request=%s)", requestedBy, requestID)

	prior := s.deps.State.Snapshot()
	ok, reason := s.deps.State.TryMarkStarting(true)
	if !ok {
		return s.deps.Gate.SendRejected(requestID, "start-rejected", reason)
	}

	s.opMutex.Lock()
	defer s.opMutex.Unlock()

	prepare := func() error {
		if err := s.deps.World.WipeWorld(); err != nil {
			return fmt.Errorf("wipe world: %w", err)
		}
		if err := s.deps.World.EnsureHardcoreMode(); err != nil {
			return fmt.Errorf("ensure hardcore mode: %w", err)
		}
		return nil
	}

	// docs/protocol-gate-manager.md 3.5節: /start clean always uses
	// force-reset, unconditionally (unlike Load, where it depends on the
	// force flag).
	return s.runSequence(ctx, requestID, "start-failed", prior, prepare, "force-reset")
}

// startResume is /start（clean無し）(architecture-manager.md 8a節): launch
// the hardcore process without touching world/, accepted purely on
// "is a process already running" (phase), never on running. This is the
// structural fix for the initial /start deadlock — see package doc comment.
func (s *ChallengeApplicationService) startResume(ctx context.Context, requestID, requestedBy string) error {
	log.Printf("application: start (resume) requested by %s (request=%s)", requestedBy, requestID)

	ok, reason := s.deps.State.TryMarkResuming()
	if !ok {
		return s.deps.Gate.SendRejected(requestID, "start-rejected", reason)
	}

	s.opMutex.Lock()
	defer s.opMutex.Unlock()

	// A failed process.Start here means the challenge itself never
	// changed (world/ was never touched) — only phase should revert,
	// running must be preserved (architecture-manager.md 8a節).
	return s.launchAndAwaitReady(ctx, requestID, "start-failed", s.deps.State.MarkDeactivated)
}

// Load implements /load <name|latest> [force] (spec 2.1節・7.3節). The
// archive-existence/latest-resolution check runs after the running check
// (so "挑戦が進行中です" always takes priority over an archive-not-found
// error, per spec 2.1節's ordering) but before opMutex, since it's a plain
// read that doesn't need to serialize against archive-request.
func (s *ChallengeApplicationService) Load(ctx context.Context, requestID string, name string, force bool, requestedBy string) error {
	log.Printf("application: load %q requested by %s (force=%v, request=%s)", name, requestedBy, force, requestID)

	prior := s.deps.State.Snapshot()
	ok, reason := s.deps.State.TryMarkStarting(force)
	if !ok {
		return s.deps.Gate.SendRejected(requestID, "load-rejected", reason)
	}

	resolvedName := name
	if name == "latest" {
		latest, err := s.deps.Archive.Latest()
		if err != nil {
			s.deps.State.Restore(prior)
			return s.deps.Gate.SendRejected(requestID, "load-rejected", "アーカイブが1件も存在しません")
		}
		resolvedName = latest
	} else {
		exists, err := s.deps.Archive.Exists(name)
		if err != nil {
			s.deps.State.Restore(prior)
			return fmt.Errorf("application: check archive %q: %w", name, err)
		}
		if !exists {
			s.deps.State.Restore(prior)
			return s.deps.Gate.SendRejected(requestID, "load-rejected", fmt.Sprintf("アーカイブ%sは存在しません", name))
		}
	}

	s.opMutex.Lock()
	defer s.opMutex.Unlock()

	prepare := func() error {
		// archive.Restore only copies (os.CopyFS refuses to overwrite
		// existing files), so — just like Start's wipe — world/ must be
		// removed first (spec 3.2節・11節: "コピー前に既存world/を削除する").
		if err := s.deps.World.WipeWorld(); err != nil {
			return fmt.Errorf("wipe world: %w", err)
		}
		if err := s.deps.Archive.Restore(resolvedName); err != nil {
			return err
		}
		// server.properties lives outside world/ and so isn't touched by
		// restoring an archive; guard it the same defensive way Start does.
		if err := s.deps.World.EnsureHardcoreMode(); err != nil {
			return fmt.Errorf("ensure hardcore mode: %w", err)
		}
		return nil
	}

	return s.runSequence(ctx, requestID, "load-failed", prior, prepare, evacuateReason(force))
}

// Deactivate implements /deactivate (spec 2.1節・7.4節,
// architecture-manager.md 8a節): stop the hardcore process without ever
// touching world/ or running. Only a running process (phase==ready) may be
// deactivated.
func (s *ChallengeApplicationService) Deactivate(ctx context.Context, requestID, requestedBy string) error {
	log.Printf("application: deactivate requested by %s (request=%s)", requestedBy, requestID)

	prior := s.deps.State.Snapshot()
	ok, reason := s.deps.State.TryMarkDeactivating()
	if !ok {
		return s.deps.Gate.SendRejected(requestID, "deactivate-rejected", reason)
	}

	s.opMutex.Lock()
	defer s.opMutex.Unlock()

	evacCtx, cancel := context.WithTimeout(ctx, s.timeouts.Evacuate)
	defer cancel()
	if err := s.deps.Gate.RequestEvacuate(evacCtx, requestID, "deactivate"); err != nil {
		// Nothing has been torn down yet.
		s.deps.State.Restore(prior)
		s.notifyFailed(requestID, "deactivate-failed", err, true)
		return fmt.Errorf("application: evacuate: %w", err)
	}

	stopCtx, cancel := context.WithTimeout(ctx, s.timeouts.ProcessStop+5*time.Second)
	defer cancel()
	if err := s.deps.Process.Stop(stopCtx, s.timeouts.ProcessStop); err != nil {
		// Whether the process actually died is unclear. Only if
		// Process.IsRunning() can confirm it's gone do we drop phase back
		// to stopped (safe to retry immediately); otherwise stay in the
		// stopping phase (stuck until an operator investigates) — a live
		// process might still be sharing world/ and the port with
		// whatever a new /start would launch (architecture-manager.md 8節).
		recovered := !s.deps.Process.IsRunning()
		if recovered {
			s.deps.State.MarkDeactivated()
			s.deps.State.MarkUnknown()
		} else {
			s.deps.State.MarkUnknown()
		}
		s.notifyFailed(requestID, "deactivate-failed", err, recovered)
		return fmt.Errorf("application: stop: %w", err)
	}

	// running is deliberately left untouched: the in-progress challenge
	// (if any) is merely paused, not finished (spec 2.1節).
	s.deps.State.MarkDeactivated()

	if err := s.deps.Gate.SendDeactivateComplete(requestID); err != nil {
		return fmt.Errorf("application: send deactivate-complete: %w", err)
	}
	return nil
}

// runSequence carries out architecture-manager.md 8節's evacuate → stop →
// prepare-world half, shared by startClean and Load, then hands off to
// launchAndAwaitReady for the process-start → ready-wait → hardcore-ready
// half shared with startResume. The caller must already hold opMutex and
// have committed the {starting, unknown} transition via
// state.TryMarkStarting. failedKind is "start-failed" or "load-failed" —
// whichever message any failure along the way should be reported as
// (docs/protocol-gate-manager.md 3.5b節).
//
// Evacuate+stop only run if a process was actually running/starting
// (prior.Phase != stopped, architecture-manager.md 8節): skipping them
// when nothing was running avoids an unnecessary evacuate-request round
// trip and matches Process.Stop's own no-op-when-absent behavior.
//
// Each failure branch picks the most accurate state.Store recovery call for
// what Manager can actually know at that point — see the inline comments.
func (s *ChallengeApplicationService) runSequence(ctx context.Context, requestID, failedKind string, prior challenge.State, prepareWorld func() error, evacReason string) error {
	if prior.Phase != challenge.PhaseStopped {
		evacCtx, cancel := context.WithTimeout(ctx, s.timeouts.Evacuate)
		if err := s.deps.Gate.RequestEvacuate(evacCtx, requestID, evacReason); err != nil {
			cancel()
			// Nothing has been torn down yet; whatever was running before is
			// presumably still fine. Undo the {starting, unknown} transition.
			s.deps.State.Restore(prior)
			s.notifyFailed(requestID, failedKind, err, true)
			return fmt.Errorf("application: evacuate: %w", err)
		}
		cancel()

		stopCtx, stopCancel := context.WithTimeout(ctx, s.timeouts.ProcessStop+5*time.Second)
		err := s.deps.Process.Stop(stopCtx, s.timeouts.ProcessStop)
		stopCancel()
		if err != nil {
			// Whether the old process actually died is unclear. Only if
			// Process.IsRunning() can confirm it's gone do we drop phase
			// back to stopped; otherwise stay in the starting phase (so a
			// bare /start·/load keeps refusing until an operator
			// investigates) rather than falsely claim it's definitely
			// stopped.
			recovered := !s.deps.Process.IsRunning()
			if recovered {
				s.deps.State.MarkDeactivated()
				s.deps.State.MarkUnknown()
			} else {
				s.deps.State.MarkUnknown()
			}
			s.notifyFailed(requestID, failedKind, err, recovered)
			return fmt.Errorf("application: stop: %w", err)
		}
	}

	if err := prepareWorld(); err != nil {
		// The old process is confirmed stopped (or never was) and no new
		// one has started: "nothing is running" is an accurate claim here.
		s.deps.State.MarkStopped()
		s.notifyFailed(requestID, failedKind, err, true)
		return fmt.Errorf("application: prepare world: %w", err)
	}

	// A failed process.Start here means the old process is confirmed gone
	// and the world has just been wiped/replaced: "running=false" is an
	// accurate claim, unlike startResume's tail where world/ was never
	// touched.
	return s.launchAndAwaitReady(ctx, requestID, failedKind, func() { s.deps.State.MarkStopped() })
}

// launchAndAwaitReady is the tail shared by runSequence (startClean/Load)
// and startResume: start the process, wait for it to report ready, then
// notify Gate (architecture-manager.md 8節 steps 6-9・8a節 steps 4-6). The
// caller must already hold opMutex. failedKind is passed straight through
// to any GateNotifier.SendFailed call this makes.
//
// onStartFailure lets each caller pick the state recovery accurate for its
// own context — see runSequence and startResume's call sites — since
// whether running should be reset to false or left untouched depends on
// whether prepareWorld already ran. Either way process.Start failing here
// means the process never actually launched, so recovered is always true
// for that branch. The ready-wait-timeout branch, in contrast, behaves
// identically regardless of caller (architecture-manager.md 8節 手順7・8a節
// 手順4): it checks Process.IsRunning() to decide recovered, exactly like
// runSequence's/Deactivate's stop-failure branch.
func (s *ChallengeApplicationService) launchAndAwaitReady(ctx context.Context, requestID, failedKind string, onStartFailure func()) error {
	s.deps.Ready.DrainReady()
	if err := s.deps.Process.Start(); err != nil {
		onStartFailure()
		s.notifyFailed(requestID, failedKind, err, true)
		return fmt.Errorf("application: start process: %w", err)
	}

	readyCtx, cancel := context.WithTimeout(ctx, s.timeouts.Ready)
	defer cancel()
	if _, err := s.deps.Ready.WaitForReady(readyCtx); err != nil {
		// The process was launched but never confirmed ready. It might
		// still come up late — the modserver adapter's `ready` handling
		// calls HandleReady independently of this wait, so nothing is lost
		// if it does. Only drop phase back to stopped if IsRunning()
		// confirms the process is actually gone (e.g. it crashed before
		// sending ready); otherwise stay in the starting phase rather than
		// risk double-launching a second process against the same world/
		// and port.
		recovered := !s.deps.Process.IsRunning()
		if recovered {
			s.deps.State.MarkDeactivated()
			s.deps.State.MarkUnknown()
		} else {
			s.deps.State.MarkUnknown()
		}
		s.notifyFailed(requestID, failedKind, err, recovered)
		return fmt.Errorf("application: wait for ready: %w", err)
	}

	// HandleReady already transitioned state to {ready, running}.
	if err := s.deps.Gate.SendHardcoreReady(requestID); err != nil {
		return fmt.Errorf("application: send hardcore-ready: %w", err)
	}
	return nil
}

// notifyFailed sends start-failed/load-failed/deactivate-failed
// (docs/protocol-gate-manager.md 3.5b節), logging rather than propagating a
// failure to do even that — the caller already has a real error to return
// for the operation itself; failing to also notify Gate about it is a
// secondary, best-effort concern.
func (s *ChallengeApplicationService) notifyFailed(requestID, kind string, err error, recovered bool) {
	if sendErr := s.deps.Gate.SendFailed(requestID, kind, err.Error(), recovered); sendErr != nil {
		log.Printf("application: send %s: %v", kind, sendErr)
	}
}

func evacuateReason(force bool) string {
	if force {
		return "force-reset"
	}
	return "reset"
}
