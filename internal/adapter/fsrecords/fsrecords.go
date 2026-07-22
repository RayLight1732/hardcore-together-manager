// Package fsrecords implements port.RecordsRepository by reading (never
// writing — that's the hardcore MOD's job) records/<challengeId>.json off
// the local filesystem (spec 3.3節・5.5節). Aggregation for /savedata and
// /senpan is domain/records's job, not this adapter's.
package fsrecords

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/RayLight1732/hardcore-together-manager/internal/domain/records"
	"github.com/RayLight1732/hardcore-together-manager/internal/port"
)

var _ port.RecordsRepository = (*Repository)(nil)

// Repository reads challenge record files under one recordsDir
// (config.Hardcore.RecordsPath(), which must match the hardcore MOD's
// storage.recordsDir — architecture-manager.md 5節).
type Repository struct {
	dir string
}

// New builds a Repository rooted at dir.
func New(dir string) *Repository {
	return &Repository{dir: dir}
}

// ReadAll parses every *.json file directly under dir. A missing dir is not
// an error (no challenge has run yet); an individual unreadable/malformed
// file is skipped rather than failing the whole read, since Manager only
// reads this data (spec 3.3節) and one corrupt file shouldn't take down
// /savedata·/senpan for every other challenge.
func (r *Repository) ReadAll() ([]records.ChallengeRecord, error) {
	entries, err := os.ReadDir(r.dir)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, fmt.Errorf("fsrecords: list %s: %w", r.dir, err)
	}

	var out []records.ChallengeRecord
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".json") {
			continue
		}
		data, err := os.ReadFile(filepath.Join(r.dir, e.Name()))
		if err != nil {
			continue
		}
		var rec records.ChallengeRecord
		if err := json.Unmarshal(data, &rec); err != nil {
			continue
		}
		out = append(out, rec)
	}
	return out, nil
}
