package osprocess

import (
	"context"
	"os"
	"path/filepath"
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

func TestStartStop_GracefulShutdown(t *testing.T) {
	dir := t.TempDir()
	ready := filepath.Join(dir, "ready")
	r := New(dir, []string{"sh", "-c", "trap 'exit 0' TERM; touch ready; sleep 30 & wait"})
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
	r := New(dir, []string{"sh", "-c", "trap '' TERM; touch ready; sleep 30 & wait"})
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
	r := New(t.TempDir(), []string{"sh", "-c", "true"})
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	if err := r.Stop(ctx, time.Second); err != nil {
		t.Fatalf("Stop on a never-started runner should be a no-op, got: %v", err)
	}
}

func TestStart_RejectsDoubleStart(t *testing.T) {
	dir := t.TempDir()
	ready := filepath.Join(dir, "ready")
	r := New(dir, []string{"sh", "-c", "touch ready; sleep 30"})
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
	r := New(dir, []string{"sh", "-c", "pwd > marker"})
	if err := r.Start(); err != nil {
		t.Fatalf("Start: %v", err)
	}

	// This command exits on its own almost immediately; just wait for the
	// file it writes rather than racing a Stop() call against it.
	waitForFile(t, marker, 2*time.Second)
}
