package osprocess

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func newTestRunner(t *testing.T, propsContent string) (*Runner, string) {
	t.Helper()
	dir := t.TempDir()
	if propsContent != "" {
		if err := os.WriteFile(filepath.Join(dir, "server.properties"), []byte(propsContent), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	return New(dir, []string{"true"}), dir
}

func readProps(t *testing.T, dir string) string {
	t.Helper()
	data, err := os.ReadFile(filepath.Join(dir, "server.properties"))
	if err != nil {
		t.Fatalf("read server.properties: %v", err)
	}
	return string(data)
}

func TestEnsureHardcoreMode_FixesFalseValue(t *testing.T) {
	r, dir := newTestRunner(t, "difficulty=easy\nhardcore=false\nlevel-seed=\n")
	if err := r.EnsureHardcoreMode(); err != nil {
		t.Fatalf("EnsureHardcoreMode: %v", err)
	}
	got := readProps(t, dir)
	if !strings.Contains(got, "hardcore=true") {
		t.Errorf("expected hardcore=true in output, got: %q", got)
	}
	if strings.Contains(got, "hardcore=false") {
		t.Errorf("hardcore=false should have been replaced, got: %q", got)
	}
	if !strings.Contains(got, "difficulty=easy") || !strings.Contains(got, "level-seed=") {
		t.Errorf("unrelated lines should be preserved, got: %q", got)
	}
}

func TestEnsureHardcoreMode_AddsKeyIfMissing(t *testing.T) {
	r, dir := newTestRunner(t, "difficulty=easy\nlevel-seed=\n")
	if err := r.EnsureHardcoreMode(); err != nil {
		t.Fatalf("EnsureHardcoreMode: %v", err)
	}
	if got := readProps(t, dir); !strings.Contains(got, "hardcore=true") {
		t.Errorf("expected hardcore=true to be appended, got: %q", got)
	}
}

func TestEnsureHardcoreMode_NoopIfAlreadyTrue(t *testing.T) {
	r, dir := newTestRunner(t, "hardcore=true\ndifficulty=hard\n")
	path := filepath.Join(dir, "server.properties")
	before, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if err := r.EnsureHardcoreMode(); err != nil {
		t.Fatalf("EnsureHardcoreMode: %v", err)
	}
	after, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if before.ModTime() != after.ModTime() {
		t.Error("file should not be rewritten when hardcore=true already")
	}
}

func TestEnsureHardcoreMode_DoesNotTouchLevelSeed(t *testing.T) {
	r, dir := newTestRunner(t, "hardcore=false\nlevel-seed=myfixedseed\n")
	if err := r.EnsureHardcoreMode(); err != nil {
		t.Fatalf("EnsureHardcoreMode: %v", err)
	}
	if got := readProps(t, dir); !strings.Contains(got, "level-seed=myfixedseed") {
		t.Errorf("level-seed must be left untouched, got: %q", got)
	}
}

func TestEnsureHardcoreMode_MissingFile(t *testing.T) {
	r, _ := newTestRunner(t, "")
	if err := r.EnsureHardcoreMode(); err == nil {
		t.Fatal("expected error for missing server.properties")
	}
}

func TestWipeWorld_RemovesDirectory(t *testing.T) {
	r, dir := newTestRunner(t, "")
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
	r, _ := newTestRunner(t, "")
	if err := r.WipeWorld(); err != nil {
		t.Fatalf("WipeWorld on absent dir should not error: %v", err)
	}
}
