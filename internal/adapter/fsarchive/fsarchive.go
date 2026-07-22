// Package fsarchive implements port.ArchiveRepository on the local
// filesystem (spec 3.2節・4節), using domain/archive's naming/collision
// rules internally.
package fsarchive

import (
	"encoding/json"
	"fmt"
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
func (r *Repository) Save(name string, elapsedTime int64, now time.Time) (string, error) {
	now = now.UTC()
	base := domainarchive.DecideBaseName(name, now)
	manual := name != ""

	resolved, err := domainarchive.ResolveName(base, manual, r.dirExists)
	if err != nil {
		return "", err
	}

	dir := filepath.Join(r.archiveDir, resolved)
	if err := os.CopyFS(filepath.Join(dir, "world"), os.DirFS(r.worldDir)); err != nil {
		return "", fmt.Errorf("fsarchive: copy world: %w", err)
	}

	if err := writeMeta(filepath.Join(dir, "meta.json"), meta{ElapsedTime: elapsedTime, CreatedAt: now}); err != nil {
		return "", err
	}

	return resolved, nil
}

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
