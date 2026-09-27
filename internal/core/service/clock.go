package service

import (
	"time"

	"github.com/jwke21/rca-transcriber/internal/core/port"
)

// SystemClock is the production Clock, backed by the real wall clock.
type SystemClock struct{}

// Now returns the current time in UTC.
func (SystemClock) Now() time.Time {
	return time.Now().UTC()
}

var _ port.Clock = SystemClock{}
