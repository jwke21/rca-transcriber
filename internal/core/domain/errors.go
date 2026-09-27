package domain

import "errors"

var (
	ErrNotFound              = errors.New("not found")
	ErrUnauthorizedCaller    = errors.New("caller is not an allowlisted engineer")
	ErrInvalidIncidentNumber = errors.New("invalid incident number")
	ErrIncidentBusy          = errors.New("incident RCA is pending or generating")
	ErrInvalidTransition     = errors.New("invalid incident status transition")
	ErrNoTranscript          = errors.New("incident has no transcript")
	ErrInvalidRCA            = errors.New("generated RCA is invalid")
	ErrSessionEnded          = errors.New("media session has ended")
	ErrStreamClosed          = errors.New("transcription stream is closed")
)
