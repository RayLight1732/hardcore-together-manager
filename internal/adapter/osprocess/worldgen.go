package osprocess

import (
	"fmt"
	"os"
)

// WipeWorld removes the world/ save folder so the next Start regenerates a
// fresh world, or so a /load restore can copy into an empty directory
// (spec 3.2節・4節). records/ lives outside world/ and is untouched by this
// call (spec 11節). It is not an error for world/ to already be absent.
func (r *Runner) WipeWorld() error {
	if err := os.RemoveAll(r.worldDir); err != nil {
		return fmt.Errorf("osprocess: wipe world: %w", err)
	}
	return nil
}

// Exists reports whether world/ is present, used by /start（clean無し）to
// reject with "ワールドが存在しません" (architecture-manager.md 3節・8a節). A
// thin read-only check, unlike WipeWorld which has side effects.
func (r *Runner) Exists() (bool, error) {
	_, err := os.Stat(r.worldDir)
	if err == nil {
		return true, nil
	}
	if os.IsNotExist(err) {
		return false, nil
	}
	return false, fmt.Errorf("osprocess: check world exists: %w", err)
}
