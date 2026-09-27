package domain

import (
	"strconv"
	"strings"
	"time"
)

// IncidentStatus is the lifecycle state of an incident's RCA pipeline.
type IncidentStatus string

const (
	IncidentStatusOpen          IncidentStatus = "open"
	IncidentStatusRCAPending    IncidentStatus = "rca_pending"
	IncidentStatusRCAGenerating IncidentStatus = "rca_generating"
	IncidentStatusRCAComplete   IncidentStatus = "rca_complete"
	IncidentStatusRCAFailed     IncidentStatus = "rca_failed"
)

// Valid reports whether s is one of the defined statuses.
func (s IncidentStatus) Valid() bool {
	switch s {
	case IncidentStatusOpen, IncidentStatusRCAPending, IncidentStatusRCAGenerating, IncidentStatusRCAComplete, IncidentStatusRCAFailed:
		return true
	default:
		return false
	}
}

// Incident is identified by the incident number the SRE enters on the keypad.
type Incident struct {
	ID             int64
	Status         IncidentStatus
	ResolutionTime *time.Time // UTC; set once, on the first confirmed resolution
	CreatedAt      time.Time  // UTC
}

// MaxIncidentNumberDigits keeps incident numbers within Postgres INTEGER range.
const MaxIncidentNumberDigits = 9

// ParseIncidentNumber validates keypad digits: after trimming whitespace, 1 to
// MaxIncidentNumberDigits characters, all 0-9, value greater than zero.
// Otherwise it returns ErrInvalidIncidentNumber.
func ParseIncidentNumber(digits string) (int64, error) {
	trimmed := strings.TrimSpace(digits)
	if len(trimmed) < 1 || len(trimmed) > MaxIncidentNumberDigits {
		return 0, ErrInvalidIncidentNumber
	}
	for _, r := range trimmed {
		if r < '0' || r > '9' {
			return 0, ErrInvalidIncidentNumber
		}
	}
	n, err := strconv.ParseInt(trimmed, 10, 64)
	if err != nil || n <= 0 {
		return 0, ErrInvalidIncidentNumber
	}
	return n, nil
}
