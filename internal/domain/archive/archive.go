// Package archive holds the pure naming/collision rules for archive/<name>/
// (spec 3.2節). The actual file copy and existence checks are I/O and
// belong to port.ArchiveRepository, implemented by adapter/fsarchive; this
// package only decides what name to use, given an injected existence check.
package archive

import (
	"errors"
	"fmt"
	"time"
)

// nameTimeFormat renders a time as the archive-name/createdAt timestamp used
// throughout specification.md (e.g. "2026-07-18T12-34-56").
const nameTimeFormat = "2006-01-02T15-04-05"

// ErrNameConflict is returned by ResolveName when a manually-specified name
// already exists (spec 3.2節: rejected, not overwritten).
var ErrNameConflict = errors.New("archive: name already exists")

// DecideBaseName returns the name to use before collision-resolution: name
// verbatim if given (manual /archive <name>), otherwise now formatted as
// spec 3.2節's timestamp (auto, e.g. boss-kill archiving). now is passed in
// rather than fetched here so the decision stays a pure function of its
// inputs — the caller reads the actual clock via port.Clock.
func DecideBaseName(name string, now time.Time) string {
	if name != "" {
		return name
	}
	return now.UTC().Format(nameTimeFormat)
}

// ResolveName decides which name to actually use under archive.dir, given
// whether name was manually specified and a callback to check if a
// candidate already exists:
//   - manual (name given): base itself if free, else ErrNameConflict —
//     never overwrite an OP-chosen name.
//   - auto (name omitted): base itself if free, else base-2, base-3, ...
//     until a free one is found (same-second boss kills, spec 3.2節).
func ResolveName(base string, manual bool, exists func(name string) (bool, error)) (string, error) {
	name := base
	for suffix := 2; ; suffix++ {
		ok, err := exists(name)
		if err != nil {
			return "", err
		}
		if !ok {
			return name, nil
		}
		if manual {
			return "", ErrNameConflict
		}
		name = fmt.Sprintf("%s-%d", base, suffix)
	}
}
