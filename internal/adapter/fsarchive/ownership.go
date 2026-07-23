package fsarchive

import (
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"syscall"
)

// matchParentOwnership walks dir (dir itself included) and chowns every
// entry to match the UID/GID of dir's parent directory.
//
// Restore's os.CopyFS recreates world/ from scratch after the caller wipes
// it, so every copied file/directory is a brand-new inode owned by whatever
// UID Manager's own process runs as. In a container setup where Manager
// runs as root but the actual hardcore server process runs as a different,
// unprivileged UID (e.g. itzg/docker-minecraft-server drops to a
// non-root UID before exec'ing java), that mismatch surfaces as a
// permission error the hardcore process can't work around — e.g. failing to
// open world/session.lock. Matching the parent directory's existing
// ownership (already set correctly by whatever set up hardcore.workDir in
// the first place) fixes this without Manager hardcoding any particular
// UID/GID itself.
func matchParentOwnership(dir string) error {
	parent := filepath.Dir(dir)
	info, err := os.Stat(parent)
	if err != nil {
		return fmt.Errorf("stat %s: %w", parent, err)
	}
	stat, ok := info.Sys().(*syscall.Stat_t)
	if !ok {
		// Platform doesn't expose uid/gid this way (only expected on
		// non-Unix, which this project doesn't target) — nothing
		// meaningful to match, so skip rather than fail.
		return nil
	}
	uid, gid := int(stat.Uid), int(stat.Gid)

	return filepath.WalkDir(dir, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		return os.Chown(path, uid, gid)
	})
}
