package fsstate

import (
	"encoding/json"
	"os"
	"path/filepath"
	"sync"
	"testing"

	"github.com/RayLight1732/hardcore-together-manager/internal/domain/challenge"
)

func newTestRepo(t *testing.T) (*Repository, string) {
	t.Helper()
	path := filepath.Join(t.TempDir(), "state.json")
	r, err := New(path)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	return r, path
}

func TestNew_NoFile_SafeDefault(t *testing.T) {
	r, _ := newTestRepo(t)
	snap := r.Snapshot()
	if snap.Phase != challenge.PhaseStopped || snap.Running != challenge.RunningFalse {
		t.Fatalf("initial snapshot = %+v, want {stopped false}", snap)
	}
}

func TestNew_RestoresPersistedRunning(t *testing.T) {
	r, path := newTestRepo(t)
	r.SetRunning(true)

	r2, err := New(path)
	if err != nil {
		t.Fatalf("New (reconstruct): %v", err)
	}
	if snap := r2.Snapshot(); snap.Phase != challenge.PhaseStopped || snap.Running != challenge.RunningTrue {
		t.Fatalf("reconstructed snapshot = %+v, want {stopped true} (Manager restart must not lose running)", snap)
	}
}

func TestNew_RestoresAfterMarkReady(t *testing.T) {
	r, path := newTestRepo(t)
	r.MarkReady(true)

	r2, err := New(path)
	if err != nil {
		t.Fatalf("New (reconstruct): %v", err)
	}
	// Phase is never persisted: a restarted Manager always starts stopped.
	if snap := r2.Snapshot(); snap.Phase != challenge.PhaseStopped || snap.Running != challenge.RunningTrue {
		t.Fatalf("reconstructed snapshot = %+v, want {stopped true}", snap)
	}
}

func TestMarkUnknown_DoesNotPersist(t *testing.T) {
	r, path := newTestRepo(t)
	r.SetRunning(true)
	r.MarkUnknown()

	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read persisted file: %v", err)
	}
	var persisted persistedState
	if err := json.Unmarshal(data, &persisted); err != nil {
		t.Fatal(err)
	}
	if !persisted.Running {
		t.Fatalf("persisted running = %v, want true (MarkUnknown must not overwrite the last known value)", persisted.Running)
	}
}

func TestTryMarkStarting_RejectsWhileRunningUnlessForce(t *testing.T) {
	s, _ := newTestRepo(t)
	s.MarkReady(true) // simulate an in-progress challenge

	ok, reason := s.TryMarkStarting(false)
	if ok {
		t.Fatal("expected rejection while running=true and force=false")
	}
	if reason == "" {
		t.Fatal("expected a non-empty reject reason")
	}
	if snap := s.Snapshot(); snap.Phase != challenge.PhaseReady || snap.Running != challenge.RunningTrue {
		t.Fatalf("rejected TryMarkStarting must not mutate state, got %+v", snap)
	}
}

func TestTryMarkStarting_RejectsWhileUnknownUnlessForce(t *testing.T) {
	s, _ := newTestRepo(t)
	s.MarkUnknown()
	ok, _ := s.TryMarkStarting(false)
	if ok {
		t.Fatal("expected rejection while running=unknown and force=false")
	}
}

func TestTryMarkStarting_AllowsWhileNotRunning(t *testing.T) {
	s, _ := newTestRepo(t)
	s.MarkReady(false)

	ok, reason := s.TryMarkStarting(false)
	if !ok {
		t.Fatalf("expected success while running=false, got reject reason %q", reason)
	}
	snap := s.Snapshot()
	if snap.Phase != challenge.PhaseStarting || snap.Running != challenge.RunningUnknown {
		t.Fatalf("snapshot after TryMarkStarting = %+v, want {starting unknown}", snap)
	}
}

func TestTryMarkStarting_ForceSkipsCheck(t *testing.T) {
	s, _ := newTestRepo(t)
	s.MarkReady(true)

	ok, _ := s.TryMarkStarting(true)
	if !ok {
		t.Fatal("expected force=true to bypass the running check")
	}
	if snap := s.Snapshot(); snap.Phase != challenge.PhaseStarting || snap.Running != challenge.RunningUnknown {
		t.Fatalf("snapshot after forced TryMarkStarting = %+v, want {starting unknown}", snap)
	}
}

func TestTryMarkStarting_RejectsMidTransitionEvenWithForce(t *testing.T) {
	s, _ := newTestRepo(t)
	s.MarkReady(false)
	if ok, _ := s.TryMarkStarting(false); !ok {
		t.Fatal("setup: expected first TryMarkStarting to succeed")
	}
	// Now phase=starting. A second call, even with force, must be rejected.
	if ok, _ := s.TryMarkStarting(true); ok {
		t.Fatal("expected mid-transition (starting) to reject even force=true")
	}
}

func TestTryMarkResuming_AllowsFromStopped(t *testing.T) {
	s, _ := newTestRepo(t)
	ok, reason := s.TryMarkResuming()
	if !ok {
		t.Fatalf("expected success from stopped, got reject reason %q", reason)
	}
	if snap := s.Snapshot(); snap.Phase != challenge.PhaseStarting {
		t.Fatalf("snapshot = %+v, want phase=starting", snap)
	}
}

func TestTryMarkResuming_IgnoresRunning(t *testing.T) {
	s, _ := newTestRepo(t)
	s.SetRunning(true) // still phase=stopped
	ok, _ := s.TryMarkResuming()
	if !ok {
		t.Fatal("expected TryMarkResuming to ignore running entirely")
	}
	if snap := s.Snapshot(); snap.Running != challenge.RunningTrue {
		t.Fatalf("running = %v, want unchanged true", snap.Running)
	}
}

func TestTryMarkResuming_RejectsWhileReady(t *testing.T) {
	s, _ := newTestRepo(t)
	s.MarkReady(true)
	ok, reason := s.TryMarkResuming()
	if ok {
		t.Fatal("expected rejection while already ready")
	}
	if reason != "既に起動しています" {
		t.Errorf("reason = %q, want 既に起動しています", reason)
	}
}

func TestTryMarkDeactivating_AllowsFromReady(t *testing.T) {
	s, _ := newTestRepo(t)
	s.MarkReady(true)
	ok, reason := s.TryMarkDeactivating()
	if !ok {
		t.Fatalf("expected success from ready, got reject reason %q", reason)
	}
	if snap := s.Snapshot(); snap.Phase != challenge.PhaseStopping || snap.Running != challenge.RunningTrue {
		t.Fatalf("snapshot = %+v, want {stopping true} (running untouched)", snap)
	}
}

func TestTryMarkDeactivating_RejectsWhileStopped(t *testing.T) {
	s, _ := newTestRepo(t)
	ok, reason := s.TryMarkDeactivating()
	if ok {
		t.Fatal("expected rejection while already stopped")
	}
	if reason != "既に停止しています" {
		t.Errorf("reason = %q, want 既に停止しています", reason)
	}
}

func TestMarkReady(t *testing.T) {
	s, _ := newTestRepo(t)
	s.MarkReady(true)
	if snap := s.Snapshot(); snap.Phase != challenge.PhaseReady || snap.Running != challenge.RunningTrue {
		t.Fatalf("snapshot = %+v, want {ready true}", snap)
	}

	s.MarkReady(false)
	if snap := s.Snapshot(); snap.Phase != challenge.PhaseReady || snap.Running != challenge.RunningFalse {
		t.Fatalf("snapshot = %+v, want {ready false}", snap)
	}
}

func TestSetRunning_LeavesPhaseUntouched(t *testing.T) {
	s, _ := newTestRepo(t)
	s.MarkReady(true)
	s.SetRunning(false)
	if snap := s.Snapshot(); snap.Phase != challenge.PhaseReady || snap.Running != challenge.RunningFalse {
		t.Fatalf("snapshot = %+v, want {ready false}", snap)
	}
}

func TestMarkUnknown_LeavesPhaseUntouched(t *testing.T) {
	s, _ := newTestRepo(t)
	s.MarkReady(true)
	s.MarkUnknown()
	if snap := s.Snapshot(); snap.Phase != challenge.PhaseReady || snap.Running != challenge.RunningUnknown {
		t.Fatalf("snapshot = %+v, want {ready unknown}", snap)
	}
}

func TestMarkStopped(t *testing.T) {
	s, _ := newTestRepo(t)
	s.MarkReady(true)
	s.MarkStopped()
	if snap := s.Snapshot(); snap.Phase != challenge.PhaseStopped || snap.Running != challenge.RunningFalse {
		t.Fatalf("snapshot = %+v, want {stopped false}", snap)
	}
}

func TestMarkDeactivated_PreservesRunning(t *testing.T) {
	s, _ := newTestRepo(t)
	s.MarkReady(true)
	s.MarkDeactivated()
	if snap := s.Snapshot(); snap.Phase != challenge.PhaseStopped || snap.Running != challenge.RunningTrue {
		t.Fatalf("snapshot = %+v, want {stopped true} (running must be preserved, unlike MarkStopped)", snap)
	}
}

func TestRestore(t *testing.T) {
	s, _ := newTestRepo(t)
	s.MarkReady(true)
	prior := s.Snapshot()

	ok, _ := s.TryMarkStarting(true)
	if !ok {
		t.Fatal("expected forced TryMarkStarting to succeed")
	}
	if snap := s.Snapshot(); snap.Phase != challenge.PhaseStarting {
		t.Fatalf("snapshot after TryMarkStarting = %+v, want phase=starting", snap)
	}

	s.Restore(prior)
	if snap := s.Snapshot(); snap != prior {
		t.Fatalf("snapshot after Restore = %+v, want %+v", snap, prior)
	}
}

// TestTryMarkStarting_ConcurrentCallsOnlyOneWins guards the exact race the
// atomic check-and-transition is meant to close: of many concurrent
// non-force TryMarkStarting calls starting from running=false, exactly one
// may succeed.
func TestTryMarkStarting_ConcurrentCallsOnlyOneWins(t *testing.T) {
	s, _ := newTestRepo(t)
	s.MarkReady(false)

	const n = 100
	var wg sync.WaitGroup
	var mu sync.Mutex
	successes := 0

	wg.Add(n)
	for i := 0; i < n; i++ {
		go func() {
			defer wg.Done()
			if ok, _ := s.TryMarkStarting(false); ok {
				mu.Lock()
				successes++
				mu.Unlock()
			}
		}()
	}
	wg.Wait()

	if successes != 1 {
		t.Fatalf("successes = %d, want exactly 1", successes)
	}
}

// TestTryMarkResuming_ConcurrentCallsOnlyOneWins mirrors the above for the
// phase-only resuming path.
func TestTryMarkResuming_ConcurrentCallsOnlyOneWins(t *testing.T) {
	s, _ := newTestRepo(t)

	const n = 100
	var wg sync.WaitGroup
	var mu sync.Mutex
	successes := 0

	wg.Add(n)
	for i := 0; i < n; i++ {
		go func() {
			defer wg.Done()
			if ok, _ := s.TryMarkResuming(); ok {
				mu.Lock()
				successes++
				mu.Unlock()
			}
		}()
	}
	wg.Wait()

	if successes != 1 {
		t.Fatalf("successes = %d, want exactly 1", successes)
	}
}
