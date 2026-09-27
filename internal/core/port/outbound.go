// outbound.go — driven ports, implemented by adapters (and by RCAService for RCATrigger)
package port

import (
	"context"
	"time"

	"github.com/jwke21/rca-transcriber/internal/core/domain"
)

// Clock abstracts time for deterministic tests.
type Clock interface {
	Now() time.Time
}

type EngineerRepository interface {
	// GetByPhoneNumber returns domain.ErrNotFound if no engineer has this number.
	GetByPhoneNumber(ctx context.Context, phoneNumber string) (domain.Engineer, error)
}

type IncidentRepository interface {
	// GetOrCreate returns the incident, inserting it with status "open" if it does not exist.
	GetOrCreate(ctx context.Context, id int64) (incident domain.Incident, created bool, err error)
	// Get returns domain.ErrNotFound if the incident does not exist.
	Get(ctx context.Context, id int64) (domain.Incident, error)
	// SetStatus sets the status unconditionally. Returns domain.ErrNotFound if missing.
	SetStatus(ctx context.Context, id int64, status domain.IncidentStatus) error
	// MarkResolved moves an "open" incident to "rca_pending" and sets resolution_time to at
	// only if it is currently NULL. Returns domain.ErrNotFound if missing and
	// domain.ErrInvalidTransition if the incident is not "open".
	MarkResolved(ctx context.Context, id int64, at time.Time) (domain.Incident, error)
	// ClaimForGeneration atomically moves "rca_pending" to "rca_generating".
	// Returns false and no error if the incident was not "rca_pending".
	ClaimForGeneration(ctx context.Context, id int64) (bool, error)
	// ListByStatus returns incidents in any of the given statuses, ordered by ID.
	ListByStatus(ctx context.Context, statuses ...domain.IncidentStatus) ([]domain.Incident, error)
}

type IncidentEventRepository interface {
	// Append inserts the event (event.ID is ignored) and returns it with its assigned ID.
	Append(ctx context.Context, event domain.IncidentEvent) (domain.IncidentEvent, error)
	// ListByIncident returns all events for the incident ordered by created_at, then ID.
	ListByIncident(ctx context.Context, incidentID int64) ([]domain.IncidentEvent, error)
}

type SpeechToText interface {
	// Open starts a new streaming transcription session.
	Open(ctx context.Context) (TranscriptionStream, error)
}

type TranscriptionStream interface {
	// SendAudio forwards raw 8 kHz mu-law audio.
	// Returns domain.ErrStreamClosed after Finish or Close.
	SendAudio(mulaw []byte) error
	// Results delivers final transcript segments. It is closed when the stream ends for any reason.
	Results() <-chan domain.TranscriptSegment
	// Finish asks the provider to flush buffered audio and close. It returns nil once Results
	// has been closed, or ctx.Err() after force-closing if ctx is done first.
	Finish(ctx context.Context) error
	// Close aborts the stream immediately. Safe to call more than once.
	Close() error
}

type RCAGenerator interface {
	// Generate produces a structured RCA from the incident's transcript (events ordered by time).
	Generate(ctx context.Context, incident domain.Incident, events []domain.IncidentEvent) (domain.RCAReport, error)
}

type RCAPublisher interface {
	// Publish commits doc.Content to doc.Path on doc.Branch and opens a PR, or updates the
	// existing branch, file and open PR. Idempotent. Returns the PR's HTML URL.
	Publish(ctx context.Context, doc domain.RCADocument) (prURL string, err error)
}

type RCATrigger interface {
	// Enqueue starts RCA generation for the incident in the background. Never blocks.
	Enqueue(incidentID int64)
}
