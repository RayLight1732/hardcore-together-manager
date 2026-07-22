// Package fsstate implements port.ChallengeStateRepository as a
// mutex-guarded store that additionally persists Running to a small local
// JSON file (architecture-manager.md 2節). Phase is never persisted — it
// always starts at stopped on construction, since Manager does not support
// re-attaching to a hardcore process that outlived a previous Manager
// instance's in-memory state (adapter/osprocess.ReapOrphan handles the
// crash-orphan case separately).
//
// Persisting Running is what breaks the original /start deadlock: without
// it, a freshly (re)started Manager had no way to distinguish "never
// started" from "in progress but disconnected" and treated both as unknown
// (safe-side reject). Package renamed from adapter/memstate.
package fsstate

import (
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"os"
	"sync"

	"github.com/RayLight1732/hardcore-together-manager/internal/domain/challenge"
	"github.com/RayLight1732/hardcore-together-manager/internal/port"
)

var _ port.ChallengeStateRepository = (*Repository)(nil)

// Repository holds {phase, running} behind one RWMutex so a caller never
// observes a torn pair, and so TryMarkStarting/TryMarkResuming/
// TryMarkDeactivating can apply their domain/challenge decision and commit
// the resulting transition atomically. Running changes are also written to
// path synchronously under the same lock, so a reader never observes memory
// and disk disagreeing.
type Repository struct {
	mu      sync.RWMutex
	phase   challenge.Phase
	running challenge.Running
	path    string
}

type persistedState struct {
	Running bool `json:"running"`
}

// New builds a Repository, restoring Running from path if it exists.
// Phase always starts at stopped (architecture-manager.md 2節: a Manager
// restart never re-attaches to a running child process). If path doesn't
// exist yet — a fresh deploy that has never completed a /start·/load —
// Running starts at false, not unknown: that's the whole point of the
// persistence, so a never-started Manager doesn't refuse its first /start
// clean the way an on-disconnect unknown would (architecture-manager.md 2節).
func New(path string) (*Repository, error) {
	r := &Repository{phase: challenge.PhaseStopped, running: challenge.RunningFalse, path: path}

	data, err := os.ReadFile(path)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return r, nil
		}
		return nil, fmt.Errorf("fsstate: read %s: %w", path, err)
	}

	var persisted persistedState
	if err := json.Unmarshal(data, &persisted); err != nil {
		return nil, fmt.Errorf("fsstate: parse %s: %w", path, err)
	}
	r.running = challenge.RunningFromBool(persisted.Running)
	return r, nil
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
// concurrent /start clean·/load calls could both observe running=false
// before either commits its transition.
func (r *Repository) TryMarkStarting(force bool) (ok bool, rejectReason string) {
	r.mu.Lock()
	defer r.mu.Unlock()

	ok, rejectReason = challenge.DecideStart(r.phase, r.running, force)
	if !ok {
		return false, rejectReason
	}
	r.phase = challenge.PhaseStarting
	r.running = challenge.RunningUnknown
	return true, ""
}

// TryMarkResuming applies challenge.DecideResume and, on success,
// transitions phase to starting. running is left exactly as-is: /start
// （clean無し）never looks at it, and the two axes (challenge progress vs.
// process lifecycle) are deliberately independent (spec 2.1節).
func (r *Repository) TryMarkResuming() (ok bool, rejectReason string) {
	r.mu.Lock()
	defer r.mu.Unlock()

	ok, rejectReason = challenge.DecideResume(r.phase)
	if !ok {
		return false, rejectReason
	}
	r.phase = challenge.PhaseStarting
	return true, ""
}

// TryMarkDeactivating applies challenge.DecideDeactivate and, on success,
// transitions phase to stopping. running is left untouched — /deactivate
// never changes the challenge's progress, only the process's lifecycle.
func (r *Repository) TryMarkDeactivating() (ok bool, rejectReason string) {
	r.mu.Lock()
	defer r.mu.Unlock()

	ok, rejectReason = challenge.DecideDeactivate(r.phase)
	if !ok {
		return false, rejectReason
	}
	r.phase = challenge.PhaseStopping
	return true, ""
}

func (r *Repository) MarkReady(running bool) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.phase = challenge.PhaseReady
	r.setRunningLocked(challenge.RunningFromBool(running))
}

func (r *Repository) SetRunning(running bool) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.setRunningLocked(challenge.RunningFromBool(running))
}

// MarkUnknown sets the running cache to the safe default on MOD⇔Manager
// disconnect while the process is still alive (docs/protocol-mod-manager.md
// 5節). Deliberately does not persist: unknown is a transient condition
// tied to this Manager instance's currently-alive process, and a future
// Manager restart resets phase to stopped regardless (architecture-manager.md
// 2節「runningの永続化」).
func (r *Repository) MarkUnknown() {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.running = challenge.RunningUnknown
}

// MarkStopped reverts to {stopped, false} — used when a /start clean·/load
// failure leaves Manager certain no process is running and no challenge is
// in progress.
func (r *Repository) MarkStopped() {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.phase = challenge.PhaseStopped
	r.setRunningLocked(challenge.RunningFalse)
}

// MarkDeactivated reverts phase to stopped only, leaving running untouched
// — used by /deactivate's success path and /start（clean無し）'s
// process-start failure, where the challenge itself never changed
// (architecture-manager.md 8a節).
func (r *Repository) MarkDeactivated() {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.phase = challenge.PhaseStopped
}

func (r *Repository) Restore(snap challenge.State) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.phase = snap.Phase
	r.setRunningLocked(snap.Running)
}

// setRunningLocked updates the in-memory value and, unless it's unknown,
// persists it to disk. Caller must hold r.mu.
func (r *Repository) setRunningLocked(running challenge.Running) {
	r.running = running
	if running == challenge.RunningUnknown {
		return
	}
	data, err := json.Marshal(persistedState{Running: running == challenge.RunningTrue})
	if err != nil {
		log.Printf("fsstate: marshal running=%v: %v", running, err)
		return
	}
	if err := os.WriteFile(r.path, data, 0o644); err != nil {
		// A failed write only affects recovery after a future Manager
		// restart, not the current process's behavior — keep the
		// in-memory value authoritative and just warn (未確定事項:
		// architecture-manager.md 14節).
		log.Printf("fsstate: persist running=%v to %s: %v", running, r.path, err)
	}
}
