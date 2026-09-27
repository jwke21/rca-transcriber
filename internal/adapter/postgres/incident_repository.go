package postgres

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/jwke21/rca-transcriber/internal/core/domain"
	"github.com/jwke21/rca-transcriber/internal/core/port"
)

var _ port.IncidentRepository = (*IncidentRepository)(nil)

const (
	incidentGetOrCreateSQL = `INSERT INTO incidents (id) VALUES ($1) ON CONFLICT (id) DO NOTHING RETURNING id, status, resolution_time, created_at`

	incidentGetSQL = `SELECT id, status, resolution_time, created_at FROM incidents WHERE id = $1`

	incidentSetStatusSQL = `UPDATE incidents SET status = $2 WHERE id = $1`

	incidentMarkResolvedSQL = `UPDATE incidents SET status = 'rca_pending', resolution_time = COALESCE(resolution_time, $2) WHERE id = $1 AND status = 'open' RETURNING id, status, resolution_time, created_at`

	incidentClaimForGenerationSQL = `UPDATE incidents SET status = 'rca_generating' WHERE id = $1 AND status = 'rca_pending'`

	incidentListByStatusSQL = `SELECT id, status, resolution_time, created_at FROM incidents WHERE status = ANY($1) ORDER BY id`
)

// IncidentRepository implements port.IncidentRepository on top of Postgres.
type IncidentRepository struct {
	db DB
}

// NewIncidentRepository constructs an IncidentRepository.
func NewIncidentRepository(db DB) *IncidentRepository {
	return &IncidentRepository{db: db}
}

// scanIncident scans one incidents row (id, status, resolution_time,
// created_at, in that order) and converts all timestamps to UTC.
func scanIncident(row rowScanner) (domain.Incident, error) {
	var (
		inc            domain.Incident
		resolutionTime *time.Time
	)

	if err := row.Scan(&inc.ID, &inc.Status, &resolutionTime, &inc.CreatedAt); err != nil {
		return domain.Incident{}, err
	}

	if resolutionTime != nil {
		utc := resolutionTime.UTC()
		inc.ResolutionTime = &utc
	}
	inc.CreatedAt = inc.CreatedAt.UTC()

	return inc, nil
}

// GetOrCreate returns the incident, inserting it with status "open" if it
// does not exist.
func (r *IncidentRepository) GetOrCreate(ctx context.Context, id int64) (domain.Incident, bool, error) {
	row := r.db.QueryRow(ctx, incidentGetOrCreateSQL, id)

	inc, err := scanIncident(row)
	if err == nil {
		return inc, true, nil
	}
	if !errors.Is(err, pgx.ErrNoRows) {
		return domain.Incident{}, false, fmt.Errorf("postgres: get or create incident %d: %w", id, err)
	}

	// The insert hit the ON CONFLICT DO NOTHING branch: the incident already exists.
	inc, err = r.Get(ctx, id)
	if err != nil {
		return domain.Incident{}, false, err
	}
	return inc, false, nil
}

// Get returns domain.ErrNotFound if the incident does not exist.
func (r *IncidentRepository) Get(ctx context.Context, id int64) (domain.Incident, error) {
	row := r.db.QueryRow(ctx, incidentGetSQL, id)

	inc, err := scanIncident(row)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return domain.Incident{}, domain.ErrNotFound
		}
		return domain.Incident{}, fmt.Errorf("postgres: get incident %d: %w", id, err)
	}

	return inc, nil
}

// SetStatus sets the status unconditionally. Returns domain.ErrNotFound if
// missing, and domain.ErrInvalidTransition if status is not a valid
// domain.IncidentStatus (checked before querying).
func (r *IncidentRepository) SetStatus(ctx context.Context, id int64, status domain.IncidentStatus) error {
	if !status.Valid() {
		return fmt.Errorf("postgres: set incident %d status to %q: %w", id, status, domain.ErrInvalidTransition)
	}

	tag, err := r.db.Exec(ctx, incidentSetStatusSQL, id, status)
	if err != nil {
		return fmt.Errorf("postgres: set incident %d status: %w", id, err)
	}
	if tag.RowsAffected() == 0 {
		return domain.ErrNotFound
	}

	return nil
}

// MarkResolved moves an "open" incident to "rca_pending" and sets
// resolution_time to at only if it is currently NULL. Returns
// domain.ErrNotFound if missing and domain.ErrInvalidTransition if the
// incident is not "open".
func (r *IncidentRepository) MarkResolved(ctx context.Context, id int64, at time.Time) (domain.Incident, error) {
	row := r.db.QueryRow(ctx, incidentMarkResolvedSQL, id, at)

	inc, err := scanIncident(row)
	if err == nil {
		return inc, nil
	}
	if !errors.Is(err, pgx.ErrNoRows) {
		return domain.Incident{}, fmt.Errorf("postgres: mark incident %d resolved: %w", id, err)
	}

	// No row matched id AND status = 'open'. Find out whether that's because
	// the incident doesn't exist, or because it exists but isn't "open".
	if _, getErr := r.Get(ctx, id); getErr != nil {
		return domain.Incident{}, getErr
	}
	return domain.Incident{}, domain.ErrInvalidTransition
}

// ClaimForGeneration atomically moves "rca_pending" to "rca_generating".
// Returns false and no error if the incident was not "rca_pending".
func (r *IncidentRepository) ClaimForGeneration(ctx context.Context, id int64) (bool, error) {
	tag, err := r.db.Exec(ctx, incidentClaimForGenerationSQL, id)
	if err != nil {
		return false, fmt.Errorf("postgres: claim incident %d for generation: %w", id, err)
	}

	return tag.RowsAffected() == 1, nil
}

// ListByStatus returns incidents in any of the given statuses, ordered by
// ID. With no statuses, it returns an empty slice without querying.
func (r *IncidentRepository) ListByStatus(ctx context.Context, statuses ...domain.IncidentStatus) ([]domain.Incident, error) {
	if len(statuses) == 0 {
		return []domain.Incident{}, nil
	}

	strStatuses := make([]string, len(statuses))
	for i, s := range statuses {
		strStatuses[i] = string(s)
	}

	rows, err := r.db.Query(ctx, incidentListByStatusSQL, strStatuses)
	if err != nil {
		return nil, fmt.Errorf("postgres: list incidents by status: %w", err)
	}
	defer rows.Close()

	var incidents []domain.Incident
	for rows.Next() {
		inc, err := scanIncident(rows)
		if err != nil {
			return nil, fmt.Errorf("postgres: list incidents by status: %w", err)
		}
		incidents = append(incidents, inc)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("postgres: list incidents by status: %w", err)
	}

	return incidents, nil
}
