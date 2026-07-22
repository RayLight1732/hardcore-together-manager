package memstate

import (
	"sync"
	"testing"

	"github.com/RayLight1732/hardcore-together-manager/internal/domain/challenge"
)

func TestNew_SafeDefault(t *testing.T) {
	s := New()
	snap := s.Snapshot()
	if snap.Phase != challenge.PhaseStopped || snap.Running != challenge.RunningUnknown {
		t.Fatalf("initial snapshot = %+v, want {stopped unknown}", snap)
	}
}

func TestTryMarkStarting_RejectsWhileRunningUnlessForce(t *testing.T) {
	s := New()
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
	s := New() // unknown by default
	ok, _ := s.TryMarkStarting(false)
	if ok {
		t.Fatal("expected rejection while running=unknown and force=false")
	}
}

func TestTryMarkStarting_AllowsWhileNotRunning(t *testing.T) {
	s := New()
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
	s := New()
	s.MarkReady(true)

	ok, _ := s.TryMarkStarting(true)
	if !ok {
		t.Fatal("expected force=true to bypass the running check")
	}
	if snap := s.Snapshot(); snap.Phase != challenge.PhaseStarting || snap.Running != challenge.RunningUnknown {
		t.Fatalf("snapshot after forced TryMarkStarting = %+v, want {starting unknown}", snap)
	}
}

func TestMarkReady(t *testing.T) {
	s := New()
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
	s := New()
	s.MarkReady(true)
	s.SetRunning(false)
	if snap := s.Snapshot(); snap.Phase != challenge.PhaseReady || snap.Running != challenge.RunningFalse {
		t.Fatalf("snapshot = %+v, want {ready false}", snap)
	}
}

func TestMarkUnknown_LeavesPhaseUntouched(t *testing.T) {
	s := New()
	s.MarkReady(true)
	s.MarkUnknown()
	if snap := s.Snapshot(); snap.Phase != challenge.PhaseReady || snap.Running != challenge.RunningUnknown {
		t.Fatalf("snapshot = %+v, want {ready unknown}", snap)
	}
}

func TestMarkStopped(t *testing.T) {
	s := New()
	s.MarkReady(true)
	s.MarkStopped()
	if snap := s.Snapshot(); snap.Phase != challenge.PhaseStopped || snap.Running != challenge.RunningFalse {
		t.Fatalf("snapshot = %+v, want {stopped false}", snap)
	}
}

func TestRestore(t *testing.T) {
	s := New()
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
	s := New()
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
