// Package fsarchive implements port.ArchiveRepository on the local
// filesystem (spec 3.2節・4節), using domain/archive's naming/collision
// rules internally.
package fsarchive

import (
	"encoding/json"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"time"

	domainarchive "github.com/RayLight1732/hardcore-together-manager/internal/domain/archive"
	"github.com/RayLight1732/hardcore-together-manager/internal/port"
)

var _ port.ArchiveRepository = (*Repository)(nil)

// meta is the content of archive/<name>/meta.json (spec 3.2節・11節).
type meta struct {
	ElapsedTime int64     `json:"elapsedTime"`
	CreatedAt   time.Time `json:"createdAt"`
}

// Repository operates on one archive.dir / hardcore world directory pair.
type Repository struct {
	archiveDir string
	worldDir   string
}

// New builds a Repository rooted at archiveDir (Manager-managed archive
// storage) and worldDir (the hardcore server's current save folder,
// config.Hardcore.WorldDir()).
func New(archiveDir, worldDir string) *Repository {
	return &Repository{archiveDir: archiveDir, worldDir: worldDir}
}

// Save copies the current world/ into archive/<name>/world/ and writes
// meta.json, resolving the final name via domain/archive's rules. The
// hardcore process is not touched here — the caller (MOD, already
// save-off'd) is responsible for that (spec 3.2節).
func (r *Repository) Save(name string, elapsedTime int64, now time.Time) (retName string, retErr error) {
	now = now.UTC()
	base := domainarchive.DecideBaseName(name, now)
	manual := name != ""

	resolved, err := domainarchive.ResolveName(base, manual, r.dirExists)
	if err != nil {
		return "", err
	}

	dir := filepath.Join(r.archiveDir, resolved)
	// dir is newly created by this call (ResolveName guarantees resolved
	// didn't already exist), so on any failure below it must be removed
	// rather than left half-populated for the next archive-request to trip
	// over (e.g. a manual name collision against a previous failed attempt).
	defer func() {
		if retErr != nil {
			os.RemoveAll(dir)
		}
	}()

	if err := copyWorldWithRetry(filepath.Join(dir, "world"), os.DirFS(r.worldDir)); err != nil {
		return "", fmt.Errorf("fsarchive: copy world: %w", err)
	}

	if err := writeMeta(filepath.Join(dir, "meta.json"), meta{ElapsedTime: elapsedTime, CreatedAt: now}); err != nil {
		return "", err
	}

	return resolved, nil
}

// copyWorldWithRetry copies fsys into worldDst via os.CopyFS, retrying a few
// times if a file present when os.CopyFS's internal fs.WalkDir listed it is
// gone by the time it gets around to opening it (os.ErrNotExist) — e.g.
// NeoForge's data/*.dat files (random_sequences.dat, data attachments, etc),
// which are written via a background worker outside the vanilla save-all
// path that save-off/save-all flush gates, so they can rarely still be
// mid-write (or mid-rename) during the copy despite the MOD having already
// save-off'd/flushed.
// os.CopyFS aborts the whole walk on the first error and never overwrites an
// existing destination file, so each retry must start from an empty
// worldDst, not resume the previous attempt. fsys is a parameter (rather
// than copyWorldWithRetry calling os.DirFS itself) so tests can inject a
// fake transient failure without touching the real filesystem.
func copyWorldWithRetry(worldDst string, fsys fs.FS) error {
	var lastErr error
	for attempt := 0; attempt < copyWorldMaxAttempts; attempt++ {
		if attempt > 0 {
			if err := os.RemoveAll(worldDst); err != nil {
				return fmt.Errorf("remove partial copy before retry %d: %w", attempt+1, err)
			}
			time.Sleep(copyWorldRetryDelay)
		}
		lastErr = os.CopyFS(worldDst, fsys)
		if lastErr == nil || !os.IsNotExist(lastErr) {
			return lastErr
		}
	}
	return lastErr
}

const (
	copyWorldMaxAttempts = 3
	copyWorldRetryDelay  = 200 * time.Millisecond
)

func (r *Repository) dirExists(name string) (bool, error) {
	return dirExists(filepath.Join(r.archiveDir, name))
}

func writeMeta(path string, m meta) error {
	data, err := json.Marshal(m)
	if err != nil {
		return fmt.Errorf("fsarchive: marshal meta.json: %w", err)
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return fmt.Errorf("fsarchive: mkdir for meta.json: %w", err)
	}
	if err := os.WriteFile(path, data, 0o644); err != nil {
		return fmt.Errorf("fsarchive: write meta.json: %w", err)
	}
	return nil
}

func dirExists(path string) (bool, error) {
	info, err := os.Stat(path)
	if err != nil {
		if os.IsNotExist(err) {
			return false, nil
		}
		return false, fmt.Errorf("fsarchive: stat %s: %w", path, err)
	}
	return info.IsDir(), nil
}
