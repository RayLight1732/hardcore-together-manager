// Package application implements ChallengeApplicationService, the single
// place every use case (/start, /load, archive-request, ready/running-changed/
// disconnect, /savedata, /senpan) is carried out (architecture-manager.md
// 8節, spec 7.3節). It depends only on internal/port and internal/domain/*,
// never on a concrete adapter — adapter/modserver and adapter/gateserver
// are thin protocol translators that call into this service, and this
// service calls back into them only through the port.GateNotifier /
// port.ReadyWaiter interfaces they happen to implement.
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

// Timeouts bounds the blocking waits inside Start/Load (14節の未確定事項:
// specific values are still open, see config.yml's timeouts section).
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

// Snapshot returns the current {phase, running}, for state-query.
func (s *ChallengeApplicationService) Snapshot() challenge.State {
	return s.deps.State.Snapshot()
}

// SaveData returns every save/death/clear event across all challenges,
// oldest first, for /savedata.
func (s *ChallengeApplicationService) SaveData() ([]records.SaveDataEntry, error) {
	all, err := s.deps.Records.ReadAll()
	if err != nil {
		return nil, err
	}
	return records.AggregateSaveData(all), nil
}

// Senpan tallies deaths by player across all challenges, for /senpan
// list|count (mode only affects Gate's display, per docs/protocol-gate-manager.md 3.7節).
func (s *ChallengeApplicationService) Senpan() ([]records.SenpanEntry, error) {
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
// (docs/protocol-mod-manager.md 5節): the running cache falls back to the
// safe default.
func (s *ChallengeApplicationService) HandleDisconnect() {
	s.deps.State.MarkUnknown()
}

// HandleArchiveRequest carries out spec 3.2節's archive-request handling:
// resolve/generate the name, copy world/, write metadata. It shares
// opMutex with Start/Load so a challenge restart and an in-flight archive
// copy never touch world/ at the same time.
func (s *ChallengeApplicationService) HandleArchiveRequest(name string, elapsedTime int64) (finalName string, err error) {
	s.opMutex.Lock()
	defer s.opMutex.Unlock()

	return s.deps.Archive.Save(name, elapsedTime, s.deps.Clock.Now())
}

// Start implements /start [force] (spec 2.1節・7.3節).
//
// architecture-manager.md 8節's original pseudocode acquired opMutex before
// the running check; doing so lets a concurrent /start block for the
// entire in-progress sequence before learning it will just be rejected,
// contradicting the claim that a second /start during "starting" is turned
// away immediately. This checks+commits the running state first
// (state.TryMarkStarting is atomic on its own, no lock needed) and only
// takes opMutex afterward, right before the file/process operations it
// actually needs to serialize with archive-request.
func (s *ChallengeApplicationService) Start(ctx context.Context, force bool, requestedBy string) error {
	log.Printf("application: start requested by %s (force=%v)", requestedBy, force)

	prior := s.deps.State.Snapshot()
	ok, reason := s.deps.State.TryMarkStarting(force)
	if !ok {
		return s.deps.Gate.SendRejected("start-rejected", reason)
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

	return s.runSequence(ctx, prior, prepare, evacuateReason(force))
}

// Load implements /load <name|latest> [force] (spec 2.1節・7.3節). The
// archive-existence/latest-resolution check runs after the running check
// (so "挑戦が進行中です" always takes priority over an archive-not-found
// error, per spec 2.1節's ordering) but before opMutex, since it's a plain
// read that doesn't need to serialize against archive-request.
func (s *ChallengeApplicationService) Load(ctx context.Context, name string, force bool, requestedBy string) error {
	log.Printf("application: load %q requested by %s (force=%v)", name, requestedBy, force)

	prior := s.deps.State.Snapshot()
	ok, reason := s.deps.State.TryMarkStarting(force)
	if !ok {
		return s.deps.Gate.SendRejected("load-rejected", reason)
	}

	resolvedName := name
	if name == "latest" {
		latest, err := s.deps.Archive.Latest()
		if err != nil {
			s.deps.State.Restore(prior)
			return s.deps.Gate.SendRejected("load-rejected", "アーカイブが1件も存在しません")
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
			return s.deps.Gate.SendRejected("load-rejected", fmt.Sprintf("アーカイブ%sは存在しません", name))
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

	return s.runSequence(ctx, prior, prepare, evacuateReason(force))
}

// runSequence carries out architecture-manager.md 8節 steps 4-8, shared by
// Start and Load: evacuate, stop the old process, prepare the world
// (branch-specific), start the new process, wait for it to report ready,
// then notify Gate. The caller must already hold opMutex and have
// committed the {starting, unknown} transition via state.TryMarkStarting.
//
// Each failure branch picks the most accurate state.Store recovery call for
// what Manager can actually know at that point — see the inline comments.
func (s *ChallengeApplicationService) runSequence(ctx context.Context, prior challenge.State, prepareWorld func() error, evacReason string) error {
	evacCtx, cancel := context.WithTimeout(ctx, s.timeouts.Evacuate)
	defer cancel()
	if err := s.deps.Gate.RequestEvacuate(evacCtx, evacReason); err != nil {
		// Nothing has been torn down yet; whatever was running before is
		// presumably still fine. Undo the {starting, unknown} transition.
		s.deps.State.Restore(prior)
		return fmt.Errorf("application: evacuate: %w", err)
	}

	stopCtx, cancel := context.WithTimeout(ctx, s.timeouts.ProcessStop+5*time.Second)
	defer cancel()
	if err := s.deps.Process.Stop(stopCtx, s.timeouts.ProcessStop); err != nil {
		// Whether the old process actually died is unclear. Stay in the
		// starting phase (so a bare /start·/load keeps refusing until an
		// operator forces it) but drop to running=unknown rather than
		// falsely claim it's definitely stopped.
		s.deps.State.MarkUnknown()
		return fmt.Errorf("application: stop: %w", err)
	}

	if err := prepareWorld(); err != nil {
		// The old process is confirmed stopped and no new one has started:
		// "nothing is running" is an accurate claim here.
		s.deps.State.MarkStopped()
		return fmt.Errorf("application: prepare world: %w", err)
	}

	s.deps.Ready.DrainReady()
	if err := s.deps.Process.Start(); err != nil {
		s.deps.State.MarkStopped()
		return fmt.Errorf("application: start process: %w", err)
	}

	readyCtx, cancel := context.WithTimeout(ctx, s.timeouts.Ready)
	defer cancel()
	if _, err := s.deps.Ready.WaitForReady(readyCtx); err != nil {
		// The process was launched but never confirmed ready. It might
		// still come up late — the modserver adapter's `ready` handling
		// calls HandleReady independently of this wait, so nothing is lost
		// if it does. Stay in the starting phase, drop to unknown rather
		// than guess.
		s.deps.State.MarkUnknown()
		return fmt.Errorf("application: wait for ready: %w", err)
	}

	// HandleReady already transitioned state to {ready, running}.
	if err := s.deps.Gate.SendHardcoreReady(); err != nil {
		return fmt.Errorf("application: send hardcore-ready: %w", err)
	}
	return nil
}

func evacuateReason(force bool) string {
	if force {
		return "force-reset"
	}
	return "reset"
}
