package postgres

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5/pgconn"

	"github.com/jwke21/rca-transcriber/internal/core/domain"
	"github.com/jwke21/rca-transcriber/internal/core/port"
)

var _ port.IncidentEventRepository = (*IncidentEventRepository)(nil)

// foreignKeyViolationCode is the Postgres SQLSTATE for a foreign-key
// violation, raised by Append when incident_id or engineer_id doesn't exist.
const foreignKeyViolationCode = "23503"

const (
	incidentEventAppendSQL = `INSERT INTO incident_events (transcription, incident_id, engineer_id, created_at) VALUES ($1, $2, $3, COALESCE($4, now())) RETURNING id, created_at`

	incidentEventListByIncidentSQL = `SELECT id, transcription, incident_id, engineer_id, created_at FROM incident_events WHERE incident_id = $1 ORDER BY created_at, id`
)

// IncidentEventRepository implements port.IncidentEventRepository on top of Postgres.
type IncidentEventRepository struct {
	db DB
}

// NewIncidentEventRepository constructs an IncidentEventRepository.
func NewIncidentEventRepository(db DB) *IncidentEventRepository {
	return &IncidentEventRepository{db: db}
}

// Append inserts the event (event.ID is ignored) and returns it with its
// assigned ID. When event.CreatedAt is the zero value, the database default
// (now()) is used instead.
func (r *IncidentEventRepository) Append(ctx context.Context, event domain.IncidentEvent) (domain.IncidentEvent, error) {
	var createdAt any
	if !event.CreatedAt.IsZero() {
		createdAt = event.CreatedAt
	}

	row := r.db.QueryRow(ctx, incidentEventAppendSQL, event.Transcription, event.IncidentID, event.EngineerID, createdAt)

	var (
		id  int64
		ts  time.Time
		err error
	)
	if err = row.Scan(&id, &ts); err != nil {
		var pgErr *pgconn.PgError
		if errors.As(err, &pgErr) && pgErr.Code == foreignKeyViolationCode {
			return domain.IncidentEvent{}, fmt.Errorf("postgres: append incident event: %w: %s", domain.ErrNotFound, pgErr.Message)
		}
		return domain.IncidentEvent{}, fmt.Errorf("postgres: append incident event: %w", err)
	}

	event.ID = id
	event.CreatedAt = ts.UTC()
	return event, nil
}

// ListByIncident returns all events for the incident ordered by created_at,
// then ID.
func (r *IncidentEventRepository) ListByIncident(ctx context.Context, incidentID int64) ([]domain.IncidentEvent, error) {
	rows, err := r.db.Query(ctx, incidentEventListByIncidentSQL, incidentID)
	if err != nil {
		return nil, fmt.Errorf("postgres: list incident events for incident %d: %w", incidentID, err)
	}
	defer rows.Close()

	var events []domain.IncidentEvent
	for rows.Next() {
		var e domain.IncidentEvent
		if err := rows.Scan(&e.ID, &e.Transcription, &e.IncidentID, &e.EngineerID, &e.CreatedAt); err != nil {
			return nil, fmt.Errorf("postgres: list incident events for incident %d: %w", incidentID, err)
		}
		e.CreatedAt = e.CreatedAt.UTC()
		events = append(events, e)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("postgres: list incident events for incident %d: %w", incidentID, err)
	}

	return events, nil
}
