package fsarchive

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

func newTestRepository(t *testing.T) (*Repository, string, string) {
	t.Helper()
	root := t.TempDir()
	archiveDir := filepath.Join(root, "archive")
	worldDir := filepath.Join(root, "hardcore", "world")

	if err := os.MkdirAll(filepath.Join(worldDir, "region"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(worldDir, "level.dat"), []byte("leveldata"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(worldDir, "region", "r.0.0.mca"), []byte("region"), 0o644); err != nil {
		t.Fatal(err)
	}

	return New(archiveDir, worldDir), archiveDir, worldDir
}

func TestSave_ManualName(t *testing.T) {
	clock := time.Date(2026, 7, 18, 12, 0, 0, 0, time.UTC)
	r, archiveDir, _ := newTestRepository(t)

	name, err := r.Save("save1", 600, clock)
	if err != nil {
		t.Fatalf("Save: %v", err)
	}
	if name != "save1" {
		t.Errorf("name = %q, want save1", name)
	}

	data, err := os.ReadFile(filepath.Join(archiveDir, "save1", "world", "level.dat"))
	if err != nil {
		t.Fatalf("read copied level.dat: %v", err)
	}
	if string(data) != "leveldata" {
		t.Errorf("level.dat content = %q, want leveldata", data)
	}
	if _, err := os.Stat(filepath.Join(archiveDir, "save1", "world", "region", "r.0.0.mca")); err != nil {
		t.Errorf("nested region file was not copied: %v", err)
	}

	m, err := readMeta(filepath.Join(archiveDir, "save1", "meta.json"))
	if err != nil {
		t.Fatalf("read meta.json: %v", err)
	}
	if m.ElapsedTime != 600 {
		t.Errorf("meta.ElapsedTime = %d, want 600", m.ElapsedTime)
	}
	if !m.CreatedAt.Equal(clock) {
		t.Errorf("meta.CreatedAt = %v, want %v", m.CreatedAt, clock)
	}
}

func TestSave_ManualNameConflictRejects(t *testing.T) {
	clock := time.Date(2026, 7, 18, 12, 0, 0, 0, time.UTC)
	r, _, _ := newTestRepository(t)

	if _, err := r.Save("save1", 1, clock); err != nil {
		t.Fatalf("first Save: %v", err)
	}
	if _, err := r.Save("save1", 2, clock); err == nil {
		t.Fatal("expected the second Save with the same manual name to fail")
	}
}

func TestSave_AutoGeneratesNameFromClock(t *testing.T) {
	clock := time.Date(2026, 7, 18, 12, 34, 56, 0, time.UTC)
	r, _, _ := newTestRepository(t)

	name, err := r.Save("", 100, clock)
	if err != nil {
		t.Fatalf("Save: %v", err)
	}
	if want := "2026-07-18T12-34-56"; name != want {
		t.Errorf("name = %q, want %q", name, want)
	}
}

func TestSave_AutoAppendsSuffixOnCollision(t *testing.T) {
	clock := time.Date(2026, 7, 18, 12, 34, 56, 0, time.UTC)
	r, _, _ := newTestRepository(t)

	first, err := r.Save("", 1, clock)
	if err != nil {
		t.Fatalf("first Save: %v", err)
	}
	second, err := r.Save("", 2, clock)
	if err != nil {
		t.Fatalf("second Save: %v", err)
	}
	third, err := r.Save("", 3, clock)
	if err != nil {
		t.Fatalf("third Save: %v", err)
	}

	if first != "2026-07-18T12-34-56" {
		t.Errorf("first = %q", first)
	}
	if second != "2026-07-18T12-34-56-2" {
		t.Errorf("second = %q, want suffix -2", second)
	}
	if third != "2026-07-18T12-34-56-3" {
		t.Errorf("third = %q, want suffix -3", third)
	}
}

func TestSave_DoesNotTouchHardcoreProcess(t *testing.T) {
	// Regression guard: Save must be pure file I/O and never shell out or
	// otherwise touch the running process — that's the whole point of spec
	// 3.2節's "プロセスを止めずに" archive design.
	clock := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	r, _, worldDir := newTestRepository(t)

	if _, err := r.Save("", 1, clock); err != nil {
		t.Fatalf("Save: %v", err)
	}
	if _, err := os.Stat(filepath.Join(worldDir, "level.dat")); err != nil {
		t.Errorf("world/ should be left in place after Save: %v", err)
	}
}
