package postgres

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/pashagolub/pgxmock/v4"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/jwke21/rca-transcriber/internal/core/domain"
)

var incidentColumns = []string{"id", "status", "resolution_time", "created_at"}

func TestIncidentRepository_GetOrCreate(t *testing.T) {
	t.Run("created", func(t *testing.T) {
		mock := newMockDB(t)
		repo := NewIncidentRepository(mock)

		createdAt := time.Date(2026, 9, 27, 12, 0, 0, 0, time.UTC)
		rows := mock.NewRows(incidentColumns).AddRow(int64(4821), "open", (*time.Time)(nil), createdAt)
		mock.ExpectQuery(exact(incidentGetOrCreateSQL)).
			WithArgs(int64(4821)).
			WillReturnRows(rows)

		inc, created, err := repo.GetOrCreate(context.Background(), 4821)
		require.NoError(t, err)
		assert.True(t, created)
		assert.Equal(t, domain.Incident{ID: 4821, Status: domain.IncidentStatusOpen, CreatedAt: createdAt}, inc)
		assert.NoError(t, mock.ExpectationsWereMet())
	})

	t.Run("existing", func(t *testing.T) {
		mock := newMockDB(t)
		repo := NewIncidentRepository(mock)

		createdAt := time.Date(2026, 9, 20, 8, 0, 0, 0, time.UTC)
		mock.ExpectQuery(exact(incidentGetOrCreateSQL)).
			WithArgs(int64(4821)).
			WillReturnRows(mock.NewRows(incidentColumns)) // no row: ON CONFLICT DO NOTHING
		mock.ExpectQuery(exact(incidentGetSQL)).
			WithArgs(int64(4821)).
			WillReturnRows(mock.NewRows(incidentColumns).AddRow(int64(4821), "rca_pending", (*time.Time)(nil), createdAt))

		inc, created, err := repo.GetOrCreate(context.Background(), 4821)
		require.NoError(t, err)
		assert.False(t, created)
		assert.Equal(t, domain.IncidentStatusRCAPending, inc.Status)
		assert.NoError(t, mock.ExpectationsWereMet())
	})

	t.Run("database error", func(t *testing.T) {
		mock := newMockDB(t)
		repo := NewIncidentRepository(mock)

		dbErr := errors.New("connection reset")
		mock.ExpectQuery(exact(incidentGetOrCreateSQL)).
			WithArgs(int64(4821)).
			WillReturnError(dbErr)

		_, _, err := repo.GetOrCreate(context.Background(), 4821)
		assert.ErrorIs(t, err, dbErr)
		assert.NoError(t, mock.ExpectationsWereMet())
	})

	t.Run("scan error", func(t *testing.T) {
		mock := newMockDB(t)
		repo := NewIncidentRepository(mock)

		rows := mock.NewRows([]string{"id"}).AddRow(int64(4821))
		mock.ExpectQuery(exact(incidentGetOrCreateSQL)).
			WithArgs(int64(4821)).
			WillReturnRows(rows)

		_, _, err := repo.GetOrCreate(context.Background(), 4821)
		require.Error(t, err)
		assert.NoError(t, mock.ExpectationsWereMet())
	})
}

func TestIncidentRepository_Get(t *testing.T) {
	t.Run("happy path", func(t *testing.T) {
		mock := newMockDB(t)
		repo := NewIncidentRepository(mock)

		resolvedAt := time.Date(2026, 9, 26, 3, 0, 0, 0, time.FixedZone("EST", -5*3600))
		createdAt := time.Date(2026, 9, 25, 0, 0, 0, 0, time.UTC)
		rows := mock.NewRows(incidentColumns).AddRow(int64(1), "rca_complete", &resolvedAt, createdAt)
		mock.ExpectQuery(exact(incidentGetSQL)).WithArgs(int64(1)).WillReturnRows(rows)

		inc, err := repo.Get(context.Background(), 1)
		require.NoError(t, err)
		require.NotNil(t, inc.ResolutionTime)
		assert.Equal(t, resolvedAt.UTC(), *inc.ResolutionTime)
		assert.Equal(t, time.UTC, inc.ResolutionTime.Location())
		assert.Equal(t, time.UTC, inc.CreatedAt.Location())
		assert.NoError(t, mock.ExpectationsWereMet())
	})

	t.Run("not found", func(t *testing.T) {
		mock := newMockDB(t)
		repo := NewIncidentRepository(mock)

		mock.ExpectQuery(exact(incidentGetSQL)).WithArgs(int64(1)).WillReturnRows(mock.NewRows(incidentColumns))

		_, err := repo.Get(context.Background(), 1)
		assert.ErrorIs(t, err, domain.ErrNotFound)
		assert.NoError(t, mock.ExpectationsWereMet())
	})

	t.Run("database error", func(t *testing.T) {
		mock := newMockDB(t)
		repo := NewIncidentRepository(mock)

		dbErr := errors.New("connection reset")
		mock.ExpectQuery(exact(incidentGetSQL)).WithArgs(int64(1)).WillReturnError(dbErr)

		_, err := repo.Get(context.Background(), 1)
		assert.ErrorIs(t, err, dbErr)
		assert.NoError(t, mock.ExpectationsWereMet())
	})

	t.Run("scan error", func(t *testing.T) {
		mock := newMockDB(t)
		repo := NewIncidentRepository(mock)

		rows := mock.NewRows([]string{"id"}).AddRow(int64(1))
		mock.ExpectQuery(exact(incidentGetSQL)).WithArgs(int64(1)).WillReturnRows(rows)

		_, err := repo.Get(context.Background(), 1)
		require.Error(t, err)
		assert.NoError(t, mock.ExpectationsWereMet())
	})
}

func TestIncidentRepository_SetStatus(t *testing.T) {
	t.Run("happy path", func(t *testing.T) {
		mock := newMockDB(t)
		repo := NewIncidentRepository(mock)

		mock.ExpectExec(exact(incidentSetStatusSQL)).
			WithArgs(int64(1), domain.IncidentStatusRCAComplete).
			WillReturnResult(pgxmock.NewResult("UPDATE", 1))

		err := repo.SetStatus(context.Background(), 1, domain.IncidentStatusRCAComplete)
		require.NoError(t, err)
		assert.NoError(t, mock.ExpectationsWereMet())
	})

	t.Run("not found", func(t *testing.T) {
		mock := newMockDB(t)
		repo := NewIncidentRepository(mock)

		mock.ExpectExec(exact(incidentSetStatusSQL)).
			WithArgs(int64(1), domain.IncidentStatusRCAComplete).
			WillReturnResult(pgxmock.NewResult("UPDATE", 0))

		err := repo.SetStatus(context.Background(), 1, domain.IncidentStatusRCAComplete)
		assert.ErrorIs(t, err, domain.ErrNotFound)
		assert.NoError(t, mock.ExpectationsWereMet())
	})

	t.Run("database error", func(t *testing.T) {
		mock := newMockDB(t)
		repo := NewIncidentRepository(mock)

		dbErr := errors.New("connection reset")
		mock.ExpectExec(exact(incidentSetStatusSQL)).
			WithArgs(int64(1), domain.IncidentStatusRCAComplete).
			WillReturnError(dbErr)

		err := repo.SetStatus(context.Background(), 1, domain.IncidentStatusRCAComplete)
		assert.ErrorIs(t, err, dbErr)
		assert.NoError(t, mock.ExpectationsWereMet())
	})

	t.Run("invalid status", func(t *testing.T) {
		mock := newMockDB(t)
		repo := NewIncidentRepository(mock)

		err := repo.SetStatus(context.Background(), 1, domain.IncidentStatus("bogus"))
		assert.ErrorIs(t, err, domain.ErrInvalidTransition)
		// No expectations were set: this asserts no query was issued.
		assert.NoError(t, mock.ExpectationsWereMet())
	})
}

func TestIncidentRepository_MarkResolved(t *testing.T) {
	resolveAt := time.Date(2026, 9, 27, 10, 0, 0, 0, time.UTC)

	t.Run("success", func(t *testing.T) {
		mock := newMockDB(t)
		repo := NewIncidentRepository(mock)

		createdAt := time.Date(2026, 9, 27, 8, 0, 0, 0, time.UTC)
		rows := mock.NewRows(incidentColumns).AddRow(int64(1), "rca_pending", &resolveAt, createdAt)
		mock.ExpectQuery(exact(incidentMarkResolvedSQL)).
			WithArgs(int64(1), resolveAt).
			WillReturnRows(rows)

		inc, err := repo.MarkResolved(context.Background(), 1, resolveAt)
		require.NoError(t, err)
		assert.Equal(t, domain.IncidentStatusRCAPending, inc.Status)
		require.NotNil(t, inc.ResolutionTime)
		assert.Equal(t, resolveAt, *inc.ResolutionTime)
		assert.NoError(t, mock.ExpectationsWereMet())
	})

	t.Run("not found", func(t *testing.T) {
		mock := newMockDB(t)
		repo := NewIncidentRepository(mock)

		mock.ExpectQuery(exact(incidentMarkResolvedSQL)).
			WithArgs(int64(1), resolveAt).
			WillReturnRows(mock.NewRows(incidentColumns))
		mock.ExpectQuery(exact(incidentGetSQL)).
			WithArgs(int64(1)).
			WillReturnRows(mock.NewRows(incidentColumns))

		_, err := repo.MarkResolved(context.Background(), 1, resolveAt)
		assert.ErrorIs(t, err, domain.ErrNotFound)
		assert.NoError(t, mock.ExpectationsWereMet())
	})

	t.Run("not open", func(t *testing.T) {
		mock := newMockDB(t)
		repo := NewIncidentRepository(mock)

		createdAt := time.Date(2026, 9, 27, 8, 0, 0, 0, time.UTC)
		mock.ExpectQuery(exact(incidentMarkResolvedSQL)).
			WithArgs(int64(1), resolveAt).
			WillReturnRows(mock.NewRows(incidentColumns))
		mock.ExpectQuery(exact(incidentGetSQL)).
			WithArgs(int64(1)).
			WillReturnRows(mock.NewRows(incidentColumns).AddRow(int64(1), "rca_complete", (*time.Time)(nil), createdAt))

		_, err := repo.MarkResolved(context.Background(), 1, resolveAt)
		assert.ErrorIs(t, err, domain.ErrInvalidTransition)
		assert.NoError(t, mock.ExpectationsWereMet())
	})

	t.Run("database error", func(t *testing.T) {
		mock := newMockDB(t)
		repo := NewIncidentRepository(mock)

		dbErr := errors.New("connection reset")
		mock.ExpectQuery(exact(incidentMarkResolvedSQL)).
			WithArgs(int64(1), resolveAt).
			WillReturnError(dbErr)

		_, err := repo.MarkResolved(context.Background(), 1, resolveAt)
		assert.ErrorIs(t, err, dbErr)
		assert.NoError(t, mock.ExpectationsWereMet())
	})

	t.Run("scan error", func(t *testing.T) {
		mock := newMockDB(t)
		repo := NewIncidentRepository(mock)

		rows := mock.NewRows([]string{"id"}).AddRow(int64(1))
		mock.ExpectQuery(exact(incidentMarkResolvedSQL)).
			WithArgs(int64(1), resolveAt).
			WillReturnRows(rows)

		_, err := repo.MarkResolved(context.Background(), 1, resolveAt)
		require.Error(t, err)
		assert.NoError(t, mock.ExpectationsWereMet())
	})
}

func TestIncidentRepository_ClaimForGeneration(t *testing.T) {
	t.Run("claimed", func(t *testing.T) {
		mock := newMockDB(t)
		repo := NewIncidentRepository(mock)

		mock.ExpectExec(exact(incidentClaimForGenerationSQL)).
			WithArgs(int64(1)).
			WillReturnResult(pgxmock.NewResult("UPDATE", 1))

		claimed, err := repo.ClaimForGeneration(context.Background(), 1)
		require.NoError(t, err)
		assert.True(t, claimed)
		assert.NoError(t, mock.ExpectationsWereMet())
	})

	t.Run("not claimed", func(t *testing.T) {
		mock := newMockDB(t)
		repo := NewIncidentRepository(mock)

		mock.ExpectExec(exact(incidentClaimForGenerationSQL)).
			WithArgs(int64(1)).
			WillReturnResult(pgxmock.NewResult("UPDATE", 0))

		claimed, err := repo.ClaimForGeneration(context.Background(), 1)
		require.NoError(t, err)
		assert.False(t, claimed)
		assert.NoError(t, mock.ExpectationsWereMet())
	})

	t.Run("database error", func(t *testing.T) {
		mock := newMockDB(t)
		repo := NewIncidentRepository(mock)

		dbErr := errors.New("connection reset")
		mock.ExpectExec(exact(incidentClaimForGenerationSQL)).
			WithArgs(int64(1)).
			WillReturnError(dbErr)

		_, err := repo.ClaimForGeneration(context.Background(), 1)
		assert.ErrorIs(t, err, dbErr)
		assert.NoError(t, mock.ExpectationsWereMet())
	})
}

func TestIncidentRepository_ListByStatus(t *testing.T) {
	t.Run("multiple rows", func(t *testing.T) {
		mock := newMockDB(t)
		repo := NewIncidentRepository(mock)

		createdAt := time.Date(2026, 9, 27, 8, 0, 0, 0, time.UTC)
		rows := mock.NewRows(incidentColumns).
			AddRow(int64(1), "rca_pending", (*time.Time)(nil), createdAt).
			AddRow(int64(2), "rca_generating", (*time.Time)(nil), createdAt)
		mock.ExpectQuery(exact(incidentListByStatusSQL)).
			WithArgs([]string{"rca_pending", "rca_generating"}).
			WillReturnRows(rows)

		incidents, err := repo.ListByStatus(context.Background(), domain.IncidentStatusRCAPending, domain.IncidentStatusRCAGenerating)
		require.NoError(t, err)
		require.Len(t, incidents, 2)
		assert.Equal(t, int64(1), incidents[0].ID)
		assert.Equal(t, int64(2), incidents[1].ID)
		assert.NoError(t, mock.ExpectationsWereMet())
	})

	t.Run("no statuses issues no query", func(t *testing.T) {
		mock := newMockDB(t)
		repo := NewIncidentRepository(mock)

		incidents, err := repo.ListByStatus(context.Background())
		require.NoError(t, err)
		assert.Empty(t, incidents)
		assert.NoError(t, mock.ExpectationsWereMet())
	})

	t.Run("database error", func(t *testing.T) {
		mock := newMockDB(t)
		repo := NewIncidentRepository(mock)

		dbErr := errors.New("connection reset")
		mock.ExpectQuery(exact(incidentListByStatusSQL)).
			WithArgs([]string{"open"}).
			WillReturnError(dbErr)

		_, err := repo.ListByStatus(context.Background(), domain.IncidentStatusOpen)
		assert.ErrorIs(t, err, dbErr)
		assert.NoError(t, mock.ExpectationsWereMet())
	})

	t.Run("scan error", func(t *testing.T) {
		mock := newMockDB(t)
		repo := NewIncidentRepository(mock)

		rows := mock.NewRows([]string{"id"}).AddRow(int64(1))
		mock.ExpectQuery(exact(incidentListByStatusSQL)).
			WithArgs([]string{"open"}).
			WillReturnRows(rows)

		_, err := repo.ListByStatus(context.Background(), domain.IncidentStatusOpen)
		require.Error(t, err)
		assert.NoError(t, mock.ExpectationsWereMet())
	})

	t.Run("rows.Err after iteration", func(t *testing.T) {
		mock := newMockDB(t)
		repo := NewIncidentRepository(mock)

		createdAt := time.Date(2026, 9, 27, 8, 0, 0, 0, time.UTC)
		iterErr := errors.New("connection reset mid-stream")
		rows := mock.NewRows(incidentColumns).
			AddRow(int64(1), "open", (*time.Time)(nil), createdAt).
			CloseError(iterErr)
		mock.ExpectQuery(exact(incidentListByStatusSQL)).
			WithArgs([]string{"open"}).
			WillReturnRows(rows)

		_, err := repo.ListByStatus(context.Background(), domain.IncidentStatusOpen)
		require.Error(t, err)
		assert.NoError(t, mock.ExpectationsWereMet())
	})
}
