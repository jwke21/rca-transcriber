// inbound.go — driving ports, implemented by core services and called by the Twilio adapter
package port

import (
	"context"

	"github.com/jwke21/rca-transcriber/internal/core/domain"
)

type RecordingStart struct {
	Engineer domain.Engineer
	Incident domain.Incident
	Resumed  bool // true when the incident already existed before this call
}

type CallService interface {
	// AuthorizeCaller returns domain.ErrUnauthorizedCaller if the number isn't allowlisted.
	AuthorizeCaller(ctx context.Context, phoneNumber string) (domain.Engineer, error)
	// BeginRecording authorizes the caller, validates the keypad digits, and finds, creates or
	// reopens the incident. Errors: ErrUnauthorizedCaller, ErrInvalidIncidentNumber, ErrIncidentBusy.
	BeginRecording(ctx context.Context, phoneNumber, digits string) (RecordingStart, error)
	// ResumeRecording is used when the SRE declines the confirmation prompt.
	// Errors: ErrUnauthorizedCaller, ErrNotFound, ErrIncidentBusy.
	ResumeRecording(ctx context.Context, phoneNumber string, incidentID int64) (RecordingStart, error)
	// ConfirmResolution returns confirmed=true only when digits == "1", after marking the
	// incident resolved and enqueueing RCA generation. Other input returns false, nil.
	ConfirmResolution(ctx context.Context, phoneNumber string, incidentID int64, digits string) (confirmed bool, err error)
}

type TranscriptionService interface {
	// StartSession opens speech-to-text for one Twilio media stream.
	StartSession(ctx context.Context, incidentID, engineerID int64) (MediaSession, error)
}

type MediaSession interface {
	// HandleAudio forwards one frame of mu-law audio. Must return quickly: no DB I/O.
	HandleAudio(ctx context.Context, mulaw []byte) error
	// HandleDTMF handles a keypress during recording. For "#" it flushes and persists the
	// remaining transcript (as End does) and returns endStream=true. Other digits are ignored.
	HandleDTMF(ctx context.Context, digit string) (endStream bool, err error)
	// End flushes remaining transcription, persists it and releases resources.
	// Idempotent and safe to call concurrently.
	End(ctx context.Context) error
}

type RCAService interface {
	RCATrigger
	// Generate runs the full pipeline synchronously for one incident.
	Generate(ctx context.Context, incidentID int64) error
	// RecoverPending re-enqueues incidents left in rca_pending or rca_generating.
	RecoverPending(ctx context.Context) error
	// Shutdown stops accepting work and waits for in-flight generations until ctx is done.
	Shutdown(ctx context.Context) error
}
