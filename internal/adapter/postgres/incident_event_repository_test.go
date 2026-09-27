package postgres

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgconn"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/jwke21/rca-transcriber/internal/core/domain"
)

func TestIncidentEventRepository_Append(t *testing.T) {
	t.Run("happy path", func(t *testing.T) {
		mock := newMockDB(t)
		repo := NewIncidentEventRepository(mock)

		spokenAt := time.Date(2026, 9, 27, 9, 30, 0, 0, time.FixedZone("EST", -5*3600))
		event := domain.IncidentEvent{
			IncidentID:    4821,
			EngineerID:    3,
			Transcription: "checking the ledger deploy",
			CreatedAt:     spokenAt,
		}

		rows := mock.NewRows([]string{"id", "created_at"}).AddRow(int64(55), spokenAt)
		mock.ExpectQuery(exact(incidentEventAppendSQL)).
			WithArgs(event.Transcription, event.IncidentID, event.EngineerID, spokenAt).
			WillReturnRows(rows)

		got, err := repo.Append(context.Background(), event)
		require.NoError(t, err)
		assert.Equal(t, int64(55), got.ID)
		assert.Equal(t, spokenAt.UTC(), got.CreatedAt)
		assert.Equal(t, time.UTC, got.CreatedAt.Location())
		assert.NoError(t, mock.ExpectationsWereMet())
	})

	t.Run("zero CreatedAt passes nil", func(t *testing.T) {
		mock := newMockDB(t)
		repo := NewIncidentEventRepository(mock)

		now := time.Date(2026, 9, 27, 9, 31, 0, 0, time.UTC)
		event := domain.IncidentEvent{
			IncidentID:    4821,
			EngineerID:    3,
			Transcription: "restarting the worker",
		}

		rows := mock.NewRows([]string{"id", "created_at"}).AddRow(int64(56), now)
		mock.ExpectQuery(exact(incidentEventAppendSQL)).
			WithArgs(event.Transcription, event.IncidentID, event.EngineerID, nil).
			WillReturnRows(rows)

		got, err := repo.Append(context.Background(), event)
		require.NoError(t, err)
		assert.Equal(t, int64(56), got.ID)
		assert.Equal(t, now, got.CreatedAt)
		assert.NoError(t, mock.ExpectationsWereMet())
	})

	t.Run("foreign key violation maps to not found", func(t *testing.T) {
		mock := newMockDB(t)
		repo := NewIncidentEventRepository(mock)

		event := domain.IncidentEvent{IncidentID: 9999, EngineerID: 3, Transcription: "x"}

		pgErr := &pgconn.PgError{Code: "23503", Message: `insert or update on table "incident_events" violates foreign key constraint`}
		mock.ExpectQuery(exact(incidentEventAppendSQL)).
			WithArgs(event.Transcription, event.IncidentID, event.EngineerID, nil).
			WillReturnError(pgErr)

		_, err := repo.Append(context.Background(), event)
		assert.ErrorIs(t, err, domain.ErrNotFound)
		assert.NoError(t, mock.ExpectationsWereMet())
	})

	t.Run("database error", func(t *testing.T) {
		mock := newMockDB(t)
		repo := NewIncidentEventRepository(mock)

		event := domain.IncidentEvent{IncidentID: 1, EngineerID: 1, Transcription: "x"}

		dbErr := errors.New("connection reset")
		mock.ExpectQuery(exact(incidentEventAppendSQL)).
			WithArgs(event.Transcription, event.IncidentID, event.EngineerID, nil).
			WillReturnError(dbErr)

		_, err := repo.Append(context.Background(), event)
		assert.ErrorIs(t, err, dbErr)
		assert.NotErrorIs(t, err, domain.ErrNotFound)
		assert.NoError(t, mock.ExpectationsWereMet())
	})

	t.Run("scan error", func(t *testing.T) {
		mock := newMockDB(t)
		repo := NewIncidentEventRepository(mock)

		event := domain.IncidentEvent{IncidentID: 1, EngineerID: 1, Transcription: "x"}

		rows := mock.NewRows([]string{"id"}).AddRow(int64(1))
		mock.ExpectQuery(exact(incidentEventAppendSQL)).
			WithArgs(event.Transcription, event.IncidentID, event.EngineerID, nil).
			WillReturnRows(rows)

		_, err := repo.Append(context.Background(), event)
		require.Error(t, err)
		assert.NoError(t, mock.ExpectationsWereMet())
	})
}

func TestIncidentEventRepository_ListByIncident(t *testing.T) {
	t.Run("happy path", func(t *testing.T) {
		mock := newMockDB(t)
		repo := NewIncidentEventRepository(mock)

		t1 := time.Date(2026, 9, 27, 9, 0, 0, 0, time.UTC)
		t2 := time.Date(2026, 9, 27, 9, 1, 0, 0, time.UTC)
		rows := mock.NewRows([]string{"id", "transcription", "incident_id", "engineer_id", "created_at"}).
			AddRow(int64(1), "first", int64(4821), int64(3), t1).
			AddRow(int64(2), "second", int64(4821), int64(3), t2)
		mock.ExpectQuery(exact(incidentEventListByIncidentSQL)).
			WithArgs(int64(4821)).
			WillReturnRows(rows)

		events, err := repo.ListByIncident(context.Background(), 4821)
		require.NoError(t, err)
		require.Len(t, events, 2)
		assert.Equal(t, "first", events[0].Transcription)
		assert.Equal(t, "second", events[1].Transcription)
		assert.Equal(t, time.UTC, events[0].CreatedAt.Location())
		assert.NoError(t, mock.ExpectationsWereMet())
	})

	t.Run("database error", func(t *testing.T) {
		mock := newMockDB(t)
		repo := NewIncidentEventRepository(mock)

		dbErr := errors.New("connection reset")
		mock.ExpectQuery(exact(incidentEventListByIncidentSQL)).
			WithArgs(int64(4821)).
			WillReturnError(dbErr)

		_, err := repo.ListByIncident(context.Background(), 4821)
		assert.ErrorIs(t, err, dbErr)
		assert.NoError(t, mock.ExpectationsWereMet())
	})

	t.Run("scan error", func(t *testing.T) {
		mock := newMockDB(t)
		repo := NewIncidentEventRepository(mock)

		rows := mock.NewRows([]string{"id"}).AddRow(int64(1))
		mock.ExpectQuery(exact(incidentEventListByIncidentSQL)).
			WithArgs(int64(4821)).
			WillReturnRows(rows)

		_, err := repo.ListByIncident(context.Background(), 4821)
		require.Error(t, err)
		assert.NoError(t, mock.ExpectationsWereMet())
	})

	t.Run("rows.Err after iteration", func(t *testing.T) {
		mock := newMockDB(t)
		repo := NewIncidentEventRepository(mock)

		iterErr := errors.New("connection reset mid-stream")
		t1 := time.Date(2026, 9, 27, 9, 0, 0, 0, time.UTC)
		rows := mock.NewRows([]string{"id", "transcription", "incident_id", "engineer_id", "created_at"}).
			AddRow(int64(1), "first", int64(4821), int64(3), t1).
			CloseError(iterErr)
		mock.ExpectQuery(exact(incidentEventListByIncidentSQL)).
			WithArgs(int64(4821)).
			WillReturnRows(rows)

		_, err := repo.ListByIncident(context.Background(), 4821)
		require.Error(t, err)
		assert.NoError(t, mock.ExpectationsWereMet())
	})
}
