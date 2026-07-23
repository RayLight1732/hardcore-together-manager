package fsarchive

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"time"
)

// ErrNotFound is returned by Restore when the named archive doesn't exist
// (spec 2.1節: "/load <name>で該当するアーカイブが存在しない場合はエラー").
var ErrNotFound = errors.New("fsarchive: not found")

// ErrNoArchives is returned by Latest when archive.dir contains no archives
// at all (spec 2.1節: "/load latestでアーカイブが1件も無い場合").
var ErrNoArchives = errors.New("fsarchive: no archives exist")

// Exists reports whether archive/<name>/ exists.
func (r *Repository) Exists(name string) (bool, error) {
	return dirExists(filepath.Join(r.archiveDir, name))
}

// Restore copies archive/<name>/world/ over worldDir. The caller must have
// already removed the current world/ (port.WorldPreparer.WipeWorld) —
// Restore only copies, matching Save's use of os.CopyFS which refuses to
// overwrite existing files.
//
// The restored files are also chowned to match worldDir's parent directory
// (hardcore.workDir) — see matchParentOwnership's doc comment for why: the
// fresh copy is otherwise owned by whatever UID Manager itself runs as,
// which can differ from the UID that actually launches the hardcore
// process (e.g. a container setup where Manager runs as root but the
// server itself runs as an unprivileged user), causing the restored world
// to be unreadable/unwritable by the server.
func (r *Repository) Restore(name string) error {
	exists, err := r.Exists(name)
	if err != nil {
		return err
	}
	if !exists {
		return ErrNotFound
	}

	src := filepath.Join(r.archiveDir, name, "world")
	if err := os.CopyFS(r.worldDir, os.DirFS(src)); err != nil {
		return fmt.Errorf("fsarchive: restore %s: %w", name, err)
	}

	if err := matchParentOwnership(r.worldDir); err != nil {
		return fmt.Errorf("fsarchive: restore %s: match ownership: %w", name, err)
	}
	return nil
}

// Latest returns the name of the archive with the newest meta.json
// createdAt, for `/load latest` (spec 2.1節・3.2節). Entries whose meta.json
// is missing or unparsable are skipped rather than failing the whole
// lookup — a single corrupt archive shouldn't block /load latest from
// finding a good one.
func (r *Repository) Latest() (string, error) {
	entries, err := os.ReadDir(r.archiveDir)
	if err != nil {
		if os.IsNotExist(err) {
			return "", ErrNoArchives
		}
		return "", fmt.Errorf("fsarchive: list %s: %w", r.archiveDir, err)
	}

	var bestName string
	var bestTime time.Time
	found := false

	for _, e := range entries {
		if !e.IsDir() {
			continue
		}
		m, err := readMeta(filepath.Join(r.archiveDir, e.Name(), "meta.json"))
		if err != nil {
			continue
		}
		if !found || m.CreatedAt.After(bestTime) {
			found = true
			bestTime = m.CreatedAt
			bestName = e.Name()
		}
	}

	if !found {
		return "", ErrNoArchives
	}
	return bestName, nil
}

func readMeta(path string) (meta, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return meta{}, err
	}
	var m meta
	if err := json.Unmarshal(data, &m); err != nil {
		return meta{}, err
	}
	return m, nil
}
