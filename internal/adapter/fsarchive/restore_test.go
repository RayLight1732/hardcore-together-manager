package fsarchive

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestExists(t *testing.T) {
	clock := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	r, _, _ := newTestRepository(t)

	if exists, err := r.Exists("save1"); err != nil || exists {
		t.Fatalf("Exists before creation = (%v, %v), want (false, nil)", exists, err)
	}
	if _, err := r.Save("save1", 1, clock); err != nil {
		t.Fatal(err)
	}
	if exists, err := r.Exists("save1"); err != nil || !exists {
		t.Fatalf("Exists after creation = (%v, %v), want (true, nil)", exists, err)
	}
}

func TestRestore_NotFound(t *testing.T) {
	r, _, _ := newTestRepository(t)
	if err := r.Restore("does-not-exist"); err != ErrNotFound {
		t.Fatalf("Restore error = %v, want ErrNotFound", err)
	}
}

func TestRestore_CopiesWorldBack(t *testing.T) {
	clock := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	r, _, worldDir := newTestRepository(t)

	if _, err := r.Save("save1", 1, clock); err != nil {
		t.Fatal(err)
	}

	// Simulate /start·/load's wipe step: the caller always removes world/
	// before calling Restore (architecture-manager.md 3節・4節).
	if err := os.RemoveAll(worldDir); err != nil {
		t.Fatal(err)
	}

	if err := r.Restore("save1"); err != nil {
		t.Fatalf("Restore: %v", err)
	}

	data, err := os.ReadFile(filepath.Join(worldDir, "level.dat"))
	if err != nil {
		t.Fatalf("read restored level.dat: %v", err)
	}
	if string(data) != "leveldata" {
		t.Errorf("restored level.dat content = %q, want leveldata", data)
	}
	if _, err := os.Stat(filepath.Join(worldDir, "region", "r.0.0.mca")); err != nil {
		t.Errorf("restored region file missing: %v", err)
	}
}

func TestLatest_NoArchives(t *testing.T) {
	r, _, _ := newTestRepository(t)
	if _, err := r.Latest(); err != ErrNoArchives {
		t.Fatalf("Latest error = %v, want ErrNoArchives", err)
	}
}

func TestLatest_PicksNewestCreatedAt(t *testing.T) {
	r, _, _ := newTestRepository(t)

	if _, err := r.Save("older", 1, time.Date(2026, 1, 1, 10, 0, 0, 0, time.UTC)); err != nil {
		t.Fatal(err)
	}
	if _, err := r.Save("newest", 1, time.Date(2026, 1, 2, 10, 0, 0, 0, time.UTC)); err != nil {
		t.Fatal(err)
	}
	if _, err := r.Save("middle", 1, time.Date(2026, 1, 1, 12, 0, 0, 0, time.UTC)); err != nil {
		t.Fatal(err)
	}

	got, err := r.Latest()
	if err != nil {
		t.Fatalf("Latest: %v", err)
	}
	if got != "newest" {
		t.Errorf("Latest() = %q, want newest", got)
	}
}

func TestLatest_SkipsCorruptEntries(t *testing.T) {
	r, archiveDir, _ := newTestRepository(t)

	if _, err := r.Save("good", 1, time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)); err != nil {
		t.Fatal(err)
	}
	// A directory with no meta.json at all should be skipped, not error out.
	if err := os.MkdirAll(filepath.Join(archiveDir, "corrupt"), 0o755); err != nil {
		t.Fatal(err)
	}

	got, err := r.Latest()
	if err != nil {
		t.Fatalf("Latest: %v", err)
	}
	if got != "good" {
		t.Errorf("Latest() = %q, want good", got)
	}
}
