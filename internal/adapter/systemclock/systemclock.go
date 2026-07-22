// Package systemclock implements port.Clock with the real wall clock.
package systemclock

import (
	"time"

	"github.com/RayLight1732/hardcore-together-manager/internal/port"
)

var _ port.Clock = Clock{}

// Clock is the real time.Now, wrapped so application code depends on
// port.Clock instead of the time package directly (keeps it fake-able in
// tests).
type Clock struct{}

func (Clock) Now() time.Time {
	return time.Now()
}
