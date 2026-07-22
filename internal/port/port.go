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
// (domain/challenge, spec 3.1節). Implemented by adapter/memstate.
type ChallengeStateRepository interface {
	// Snapshot returns the current state, for state-query responses.
	Snapshot() challenge.State

	// TryMarkStarting atomically applies challenge.DecideStart and, if
	// allowed, transitions to {starting, unknown} in the same critical
	// section — see adapter/memstate's doc comment for why this must be
	// atomic rather than a separate check-then-transition.
	TryMarkStarting(force bool) (ok bool, rejectReason string)

	// MarkReady transitions to {ready, running} on the hardcore MOD's
	// `ready` signal (docs/protocol-mod-manager.md 3.1節).
	MarkReady(running bool)

	// SetRunning updates only the running cache on `running-changed`
	// (docs/protocol-mod-manager.md 3.2節); phase is left untouched.
	SetRunning(running bool)

	// MarkUnknown sets the running cache to the safe default on
	// MOD⇔Manager disconnect (docs/protocol-mod-manager.md 5節).
	MarkUnknown()

	// MarkStopped reverts to {stopped, false} — used when a /start·/load
	// failure leaves Manager certain no process is running.
	MarkStopped()

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
// 3節). Implemented by adapter/gateserver.
type GateNotifier interface {
	RequestEvacuate(ctx context.Context, reason string) error
	SendHardcoreReady() error
	SendRejected(kind, reason string) error
}

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
