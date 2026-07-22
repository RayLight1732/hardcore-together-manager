// Package memstate implements port.ChallengeStateRepository as an in-memory,
// mutex-guarded store (architecture-manager.md 2節).
package memstate

import (
	"sync"

	"github.com/RayLight1732/hardcore-together-manager/internal/domain/challenge"
	"github.com/RayLight1732/hardcore-together-manager/internal/port"
)

var _ port.ChallengeStateRepository = (*Repository)(nil)

// Repository holds {phase, running} behind one RWMutex so a caller never
// observes a torn pair, and so TryMarkStarting can apply
// challenge.DecideStart and commit the resulting transition atomically.
type Repository struct {
	mu      sync.RWMutex
	phase   challenge.Phase
	running challenge.Running
}

// New returns a Repository in the safe-default initial state:
// stopped/unknown, as if Manager had just started (spec 3.1節).
func New() *Repository {
	return &Repository{phase: challenge.PhaseStopped, running: challenge.RunningUnknown}
}

func (r *Repository) Snapshot() challenge.State {
	r.mu.RLock()
	defer r.mu.RUnlock()
	return challenge.State{Phase: r.phase, Running: r.running}
}

// TryMarkStarting applies challenge.DecideStart and, on success,
// transitions to {starting, unknown} inside the same lock acquisition.
// Doing the check and the transition atomically (rather than a Snapshot
// followed by a later, separate mutation) closes the race where two
// concurrent /start·/load calls could both observe running=false before
// either commits its transition.
func (r *Repository) TryMarkStarting(force bool) (ok bool, rejectReason string) {
	r.mu.Lock()
	defer r.mu.Unlock()

	ok, rejectReason = challenge.DecideStart(r.running, force)
	if !ok {
		return false, rejectReason
	}
	r.phase = challenge.PhaseStarting
	r.running = challenge.RunningUnknown
	return true, ""
}

func (r *Repository) MarkReady(running bool) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.phase = challenge.PhaseReady
	r.running = challenge.RunningFromBool(running)
}

func (r *Repository) SetRunning(running bool) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.running = challenge.RunningFromBool(running)
}

func (r *Repository) MarkUnknown() {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.running = challenge.RunningUnknown
}

func (r *Repository) MarkStopped() {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.phase = challenge.PhaseStopped
	r.running = challenge.RunningFalse
}

func (r *Repository) Restore(snap challenge.State) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.phase = snap.Phase
	r.running = snap.Running
}
