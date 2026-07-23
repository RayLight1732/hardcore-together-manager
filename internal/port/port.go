// Package port declares the interfaces application.ChallengeApplicationService
// depends on. Each is implemented by exactly one adapter in this codebase
// (see the doc comment on each interface), but the application layer never
// imports those adapter packages directly — only these interfaces, so it
// can be tested with fakes and so adapters can be swapped without touching
// business logic.
package port

import (
	"context"
	"time"

	"github.com/RayLight1732/hardcore-together-manager/internal/domain/challenge"
	"github.com/RayLight1732/hardcore-together-manager/internal/domain/records"
)

// ChallengeStateRepository is the current {phase, running} store
// (domain/challenge, spec 3.1節). Implemented by adapter/fsstate, which
// additionally persists Running to disk so it survives a Manager restart
// (architecture-manager.md 2節).
type ChallengeStateRepository interface {
	// Snapshot returns the current state, for state-query responses.
	Snapshot() challenge.State

	// TryMarkStarting atomically applies challenge.DecideStart and, if
	// allowed, transitions to {starting, unknown} in the same critical
	// section — see adapter/fsstate's doc comment for why this must be
	// atomic rather than a separate check-then-transition. Used by
	// /start clean and /load.
	TryMarkStarting(force bool) (ok bool, rejectReason string)

	// TryMarkResuming atomically applies challenge.DecideResume and, if
	// allowed, transitions phase to starting (running is left untouched —
	// /start（clean無し）never looks at it). Used by /start（clean無し）
	// (architecture-manager.md 8a節).
	TryMarkResuming() (ok bool, rejectReason string)

	// TryMarkDeactivating atomically applies challenge.DecideDeactivate
	// and, if allowed, transitions phase to stopping. Used by /deactivate
	// (architecture-manager.md 8a節).
	TryMarkDeactivating() (ok bool, rejectReason string)

	// MarkReady transitions to {ready, running} on the hardcore MOD's
	// `ready` signal (docs/protocol-mod-manager.md 3.1節).
	MarkReady(running bool)

	// SetRunning updates only the running cache on `running-changed`
	// (docs/protocol-mod-manager.md 3.2節); phase is left untouched.
	SetRunning(running bool)

	// MarkUnknown sets the running cache to the safe default on
	// MOD⇔Manager disconnect while the process is still alive
	// (docs/protocol-mod-manager.md 5節).
	MarkUnknown()

	// MarkStopped reverts to {stopped, false} — used when a /start
	// clean·/load failure leaves Manager certain no process is running
	// and no challenge is in progress.
	MarkStopped()

	// MarkDeactivated reverts phase to stopped only, leaving running
	// untouched — used by /deactivate's success path and /start（clean無し）'s
	// process-start failure, where the challenge itself (running) never
	// changed (architecture-manager.md 8a節).
	MarkDeactivated()

	// Restore overwrites {phase, running} with a previously-taken
	// State, undoing a TryMarkStarting whose secondary checks (e.g.
	// Load's archive-existence check) later failed.
	Restore(snap challenge.State)
}

// ProcessRunner starts/stops the hardcore server child process (spec 1節).
// Implemented by adapter/osprocess.
type ProcessRunner interface {
	Start() error
	Stop(ctx context.Context, killTimeout time.Duration) error

	// IsRunning reports whether a process launched by this Runner is
	// currently alive, distinct from Running (the challenge-in-progress
	// value): used by HandleDisconnect to tell "the hardcore process
	// itself died" apart from "only the TCP connection dropped"
	// (docs/protocol-mod-manager.md 5節).
	IsRunning() bool
}

// WorldPreparer prepares world/ and server.properties for a fresh /start or
// a /load restore (architecture-manager.md 3節). Implemented by
// adapter/osprocess.
type WorldPreparer interface {
	// WipeWorld removes world/ so the caller can either let a fresh world
	// generate (Start) or copy an archive into it (Load).
	WipeWorld() error
	// EnsureHardcoreMode makes sure server.properties has hardcore=true.
	EnsureHardcoreMode() error
	// Exists reports whether world/ is present — used by /start（clean無し）
	// to reject with "ワールドが存在しません" (architecture-manager.md 3節・8a節).
	Exists() (bool, error)
}

// ArchiveRepository manages archive/<name>/ (spec 3.2節). Implemented by
// adapter/fsarchive, using domain/archive's naming rules internally.
type ArchiveRepository interface {
	// Exists reports whether archive/<name>/ exists.
	Exists(name string) (bool, error)
	// Latest returns the name of the archive with the newest createdAt.
	Latest() (string, error)
	// Restore copies archive/<name>/world/ over the current world/. The
	// caller must have already wiped world/ (WorldPreparer.WipeWorld).
	Restore(name string) error
	// Save copies the current world/ into archive/<name-or-generated>/ and
	// writes its metadata, resolving the final name per domain/archive's
	// rules (name given: reject on collision; name empty: auto-generate
	// from now and suffix on collision). Returns the name actually used.
	Save(name string, elapsedTime int64, now time.Time) (finalName string, err error)
}

// RecordsRepository reads (never writes) records/<challengeId>.json (spec
// 3.3節). Implemented by adapter/fsrecords; aggregation itself is
// domain/records's job, not this port's.
type RecordsRepository interface {
	ReadAll() ([]records.ChallengeRecord, error)
}

// GateNotifier is how application sends Gate the messages that only make
// sense in reply to something Gate itself sent (docs/protocol-gate-manager.md
// 3節). Implemented by adapter/gateserver. Every method takes requestID —
// the UUID Gate attached to the request being answered — and echoes it back
// verbatim; Manager never interprets its value (docs/protocol-gate-manager.md
// 1節, architecture-manager.md 7節).
type GateNotifier interface {
	RequestEvacuate(ctx context.Context, requestID, reason string) error
	SendHardcoreReady(requestID string) error
	// SendRejected sends kind (one of the Kind*Rejected constants below)
	// with reason.
	SendRejected(requestID, kind, reason string) error
	// SendDeactivateComplete notifies Gate that /deactivate's process stop
	// has completed (docs/protocol-gate-manager.md 3.5a節).
	SendDeactivateComplete(requestID string) error
	// SendFailed notifies Gate that an accepted start/load/deactivate
	// failed partway through (docs/protocol-gate-manager.md 3.5b節). kind
	// is one of the Kind*Failed constants below. recovered reports whether
	// Manager confirmed the hardcore process is not running and reset
	// phase back to stopped (safe to retry immediately) or had to leave
	// phase mid-transition because it couldn't confirm that
	// (architecture-manager.md 8節・8a節).
	SendFailed(requestID, kind, reason string, recovered bool) error
}

// Kind* are the valid values for GateNotifier.SendRejected/SendFailed's
// kind parameter — also the wire value of the resulting message's "type"
// field (docs/protocol-gate-manager.md 3.4節・3.5b節). Defined once here,
// rather than as string literals scattered across application (the only
// caller) and adapter/gateserver (which just forwards kind into the
// message's Type field verbatim), so a typo becomes a compile error instead
// of a silently-wrong wire message no test happens to cover.
const (
	KindStartRejected      = "start-rejected"
	KindLoadRejected       = "load-rejected"
	KindDeactivateRejected = "deactivate-rejected"

	KindStartFailed      = "start-failed"
	KindLoadFailed       = "load-failed"
	KindDeactivateFailed = "deactivate-failed"
)

// ReadyWaiter lets application block until the hardcore MOD's `ready`
// signal for a just-launched process arrives (architecture-manager.md 8節).
// Implemented by adapter/modserver.
type ReadyWaiter interface {
	WaitForReady(ctx context.Context) (running bool, err error)
	DrainReady()
}

// Clock abstracts time.Now so archive naming/createdAt generation
// (domain/archive.DecideBaseName) stays testable. Implemented by
// adapter/systemclock.
type Clock interface {
	Now() time.Time
}
