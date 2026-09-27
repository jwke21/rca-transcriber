package domain

import "time"

// IncidentEvent is one finished transcript line.
type IncidentEvent struct {
	ID            int64
	IncidentID    int64
	EngineerID    int64
	Transcription string
	CreatedAt     time.Time // when the words were spoken, UTC
}
