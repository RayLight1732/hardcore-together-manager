package osprocess

import (
	"os"
	"path/filepath"
	"testing"
)

func newTestRunner(t *testing.T) (*Runner, string) {
	t.Helper()
	dir := t.TempDir()
	return New(dir, []string{"true"}, ""), dir
}

func TestWipeWorld_RemovesDirectory(t *testing.T) {
	r, dir := newTestRunner(t)
	worldDir := filepath.Join(dir, "world")
	if err := os.MkdirAll(filepath.Join(worldDir, "region"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(worldDir, "level.dat"), []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}

	if err := r.WipeWorld(); err != nil {
		t.Fatalf("WipeWorld: %v", err)
	}
	if _, err := os.Stat(worldDir); !os.IsNotExist(err) {
		t.Errorf("world dir should be gone, stat err = %v", err)
	}
}

func TestWipeWorld_NoopIfMissing(t *testing.T) {
	r, _ := newTestRunner(t)
	if err := r.WipeWorld(); err != nil {
		t.Fatalf("WipeWorld on absent dir should not error: %v", err)
	}
}

func TestExists_FalseWhenMissing(t *testing.T) {
	r, _ := newTestRunner(t)
	exists, err := r.Exists()
	if err != nil {
		t.Fatalf("Exists: %v", err)
	}
	if exists {
		t.Fatal("expected Exists to be false when world/ has never been created")
	}
}

func TestExists_TrueWhenPresent(t *testing.T) {
	r, dir := newTestRunner(t)
	if err := os.MkdirAll(filepath.Join(dir, "world"), 0o755); err != nil {
		t.Fatal(err)
	}
	exists, err := r.Exists()
	if err != nil {
		t.Fatalf("Exists: %v", err)
	}
	if !exists {
		t.Fatal("expected Exists to be true once world/ has been created")
	}
}
