package osprocess

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"
)

// waitForFile polls until path exists or the deadline passes, so tests don't
// race the shell script's startup (installing a trap, in particular, takes a
// moment relative to how fast Go can call Stop after Start).
func waitForFile(t *testing.T, path string, timeout time.Duration) {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for {
		if _, err := os.Stat(path); err == nil {
			return
		}
		if time.Now().After(deadline) {
			t.Fatalf("%s was never created within %v", path, timeout)
		}
		time.Sleep(10 * time.Millisecond)
	}
}

func waitForFileGone(t *testing.T, path string, timeout time.Duration) {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for {
		if _, err := os.Stat(path); os.IsNotExist(err) {
			return
		}
		if time.Now().After(deadline) {
			t.Fatalf("%s still exists after %v", path, timeout)
		}
		time.Sleep(10 * time.Millisecond)
	}
}

func TestStartStop_GracefulShutdown(t *testing.T) {
	dir := t.TempDir()
	ready := filepath.Join(dir, "ready")
	r := New(dir, []string{"sh", "-c", "trap 'exit 0' TERM; touch ready; sleep 30 & wait"}, "")
	if err := r.Start(); err != nil {
		t.Fatalf("Start: %v", err)
	}
	waitForFile(t, ready, 2*time.Second)

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	start := time.Now()
	if err := r.Stop(ctx, 3*time.Second); err != nil {
		t.Fatalf("Stop: %v", err)
	}
	if elapsed := time.Since(start); elapsed >= 3*time.Second {
		t.Fatalf("Stop took %v, expected a quick graceful exit well under the 3s kill timeout", elapsed)
	}
}

func TestStop_EscalatesToSigkillWhenTermIgnored(t *testing.T) {
	dir := t.TempDir()
	ready := filepath.Join(dir, "ready")
	r := New(dir, []string{"sh", "-c", "trap '' TERM; touch ready; sleep 30 & wait"}, "")
	if err := r.Start(); err != nil {
		t.Fatalf("Start: %v", err)
	}
	waitForFile(t, ready, 2*time.Second)

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	start := time.Now()
	killTimeout := 500 * time.Millisecond
	if err := r.Stop(ctx, killTimeout); err != nil {
		t.Fatalf("Stop: %v", err)
	}
	if elapsed := time.Since(start); elapsed < killTimeout {
		t.Fatalf("Stop returned in %v, expected to wait out the kill timeout before escalating", elapsed)
	}
}

func TestStop_NoopWhenNotRunning(t *testing.T) {
	r := New(t.TempDir(), []string{"sh", "-c", "true"}, "")
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	if err := r.Stop(ctx, time.Second); err != nil {
		t.Fatalf("Stop on a never-started runner should be a no-op, got: %v", err)
	}
}

func TestStart_RejectsDoubleStart(t *testing.T) {
	dir := t.TempDir()
	ready := filepath.Join(dir, "ready")
	r := New(dir, []string{"sh", "-c", "touch ready; sleep 30"}, "")
	if err := r.Start(); err != nil {
		t.Fatalf("Start: %v", err)
	}
	waitForFile(t, ready, 2*time.Second)
	defer func() {
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		defer cancel()
		_ = r.Stop(ctx, time.Second)
	}()

	if err := r.Start(); err == nil {
		t.Fatal("expected error starting a second process while the first is still running")
	}
}

func TestStart_UsesWorkDir(t *testing.T) {
	dir := t.TempDir()
	marker := filepath.Join(dir, "marker")
	r := New(dir, []string{"sh", "-c", "pwd > marker"}, "")
	if err := r.Start(); err != nil {
		t.Fatalf("Start: %v", err)
	}

	// This command exits on its own almost immediately; just wait for the
	// file it writes rather than racing a Stop() call against it.
	waitForFile(t, marker, 2*time.Second)
}

func TestIsRunning(t *testing.T) {
	dir := t.TempDir()
	ready := filepath.Join(dir, "ready")
	r := New(dir, []string{"sh", "-c", "trap 'exit 0' TERM; touch ready; sleep 30 & wait"}, "")
	if r.IsRunning() {
		t.Fatal("IsRunning must be false before Start")
	}

	if err := r.Start(); err != nil {
		t.Fatalf("Start: %v", err)
	}
	waitForFile(t, ready, 2*time.Second)
	if !r.IsRunning() {
		t.Fatal("IsRunning must be true once started")
	}

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := r.Stop(ctx, 3*time.Second); err != nil {
		t.Fatalf("Stop: %v", err)
	}
	if r.IsRunning() {
		t.Fatal("IsRunning must be false after Stop")
	}
}

func TestPIDFile_WrittenOnStartAndRemovedOnStop(t *testing.T) {
	dir := t.TempDir()
	ready := filepath.Join(dir, "ready")
	pidFile := filepath.Join(dir, "hardcore.pid")
	r := New(dir, []string{"sh", "-c", "trap 'exit 0' TERM; touch ready; sleep 30 & wait"}, pidFile)
	if err := r.Start(); err != nil {
		t.Fatalf("Start: %v", err)
	}
	waitForFile(t, ready, 2*time.Second)
	waitForFile(t, pidFile, 2*time.Second)

	data, err := os.ReadFile(pidFile)
	if err != nil {
		t.Fatalf("read pid file: %v", err)
	}
	if _, err := strconv.Atoi(strings.TrimSpace(string(data))); err != nil {
		t.Fatalf("pid file content %q is not a valid PID", data)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := r.Stop(ctx, 3*time.Second); err != nil {
		t.Fatalf("Stop: %v", err)
	}
	waitForFileGone(t, pidFile, 2*time.Second)
}

func TestReapOrphan_NoPidFile_NoOp(t *testing.T) {
	r := New(t.TempDir(), nil, filepath.Join(t.TempDir(), "nonexistent.pid"))
	if err := r.ReapOrphan(time.Second); err != nil {
		t.Fatalf("ReapOrphan: %v", err)
	}
}

func TestReapOrphan_StaleDeadPID_RemovesFile(t *testing.T) {
	dir := t.TempDir()
	pidFile := filepath.Join(dir, "hardcore.pid")

	// Launch and fully wait for a short-lived process to get a PID that is
	// guaranteed dead, then write it to the pid file directly (simulating a
	// leftover file from a process that already exited).
	cmd := exec.Command("sh", "-c", "exit 0")
	if err := cmd.Run(); err != nil {
		t.Fatalf("run helper: %v", err)
	}
	deadPID := cmd.Process.Pid
	if err := os.WriteFile(pidFile, []byte(strconv.Itoa(deadPID)), 0o644); err != nil {
		t.Fatal(err)
	}

	r := New(dir, nil, pidFile)
	if err := r.ReapOrphan(500 * time.Millisecond); err != nil {
		t.Fatalf("ReapOrphan: %v", err)
	}
	if _, err := os.Stat(pidFile); !os.IsNotExist(err) {
		t.Fatalf("expected stale pid file to be removed, stat err = %v", err)
	}
}

func TestReapOrphan_AliveProcess_TerminatesAndRemovesFile(t *testing.T) {
	dir := t.TempDir()
	pidFile := filepath.Join(dir, "hardcore.pid")

	// A real, currently-alive process not launched via this Runner —
	// simulating a hardcore process orphaned by a crashed previous Manager
	// instance.
	orphan := exec.Command("sh", "-c", "trap 'exit 0' TERM; sleep 30 & wait")
	if err := orphan.Start(); err != nil {
		t.Fatalf("start orphan: %v", err)
	}
	t.Cleanup(func() { _ = orphan.Process.Kill() })
	// This test's own process is orphan's real parent (unlike production,
	// where the reparented-to-init orphan's actual owner reaps it), so it
	// must Wait() concurrently or the terminated process stays a zombie —
	// which a signal-0 liveness probe still reports as "alive" — forever.
	go func() { _, _ = orphan.Process.Wait() }()

	if err := os.WriteFile(pidFile, []byte(strconv.Itoa(orphan.Process.Pid)), 0o644); err != nil {
		t.Fatal(err)
	}

	r := New(dir, nil, pidFile)
	done := make(chan error, 1)
	go func() { done <- r.ReapOrphan(3 * time.Second) }()

	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("ReapOrphan: %v", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("ReapOrphan never returned")
	}

	if _, err := os.Stat(pidFile); !os.IsNotExist(err) {
		t.Fatalf("expected pid file to be removed after reaping, stat err = %v", err)
	}
	if processAlive(orphan.Process) {
		t.Fatal("expected the orphaned process to have been terminated")
	}
}
