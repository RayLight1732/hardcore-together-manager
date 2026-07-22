package osprocess

import (
	"fmt"
	"os"
	"strings"
)

const hardcoreKey = "hardcore"

// EnsureHardcoreMode makes sure server.properties has hardcore=true,
// rewriting the file only if the value is missing or false. level-seed is
// intentionally left untouched — leaving it blank (so every fresh world
// gets a random seed) is the initial setup's responsibility, not something
// Manager enforces on every /start (architecture-manager.md 3節).
func (r *Runner) EnsureHardcoreMode() error {
	data, err := os.ReadFile(r.serverPropertiesPath)
	if err != nil {
		return fmt.Errorf("osprocess: read server.properties: %w", err)
	}

	lines := strings.Split(string(data), "\n")
	const wantLine = hardcoreKey + "=true"
	found := false
	changed := false

	for i, line := range lines {
		trimmed := strings.TrimRight(line, "\r")
		key, _, ok := strings.Cut(trimmed, "=")
		if !ok || key != hardcoreKey {
			continue
		}
		found = true
		if trimmed != wantLine {
			lines[i] = wantLine
			changed = true
		}
		break
	}

	if !found {
		lines = append(lines, wantLine)
		changed = true
	}

	if !changed {
		return nil
	}

	out := strings.Join(lines, "\n")
	if err := os.WriteFile(r.serverPropertiesPath, []byte(out), 0o644); err != nil {
		return fmt.Errorf("osprocess: write server.properties: %w", err)
	}
	return nil
}

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
