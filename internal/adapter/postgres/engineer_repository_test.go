package postgres

import (
	"context"
	"errors"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/jwke21/rca-transcriber/internal/core/domain"
)

func TestEngineerRepository_GetByPhoneNumber(t *testing.T) {
	t.Run("happy path", func(t *testing.T) {
		mock := newMockDB(t)
		repo := NewEngineerRepository(mock)

		rows := mock.NewRows([]string{"id", "phone_number"}).AddRow(int64(7), "+15555550123")
		mock.ExpectQuery(exact(engineerGetByPhoneNumberSQL)).
			WithArgs("+15555550123").
			WillReturnRows(rows)

		got, err := repo.GetByPhoneNumber(context.Background(), "+15555550123")
		require.NoError(t, err)
		assert.Equal(t, domain.Engineer{ID: 7, PhoneNumber: "+15555550123"}, got)
		assert.NoError(t, mock.ExpectationsWereMet())
	})

	t.Run("not found", func(t *testing.T) {
		mock := newMockDB(t)
		repo := NewEngineerRepository(mock)

		mock.ExpectQuery(exact(engineerGetByPhoneNumberSQL)).
			WithArgs("+15555550999").
			WillReturnRows(mock.NewRows([]string{"id", "phone_number"}))

		_, err := repo.GetByPhoneNumber(context.Background(), "+15555550999")
		assert.ErrorIs(t, err, domain.ErrNotFound)
		assert.NoError(t, mock.ExpectationsWereMet())
	})

	t.Run("database error", func(t *testing.T) {
		mock := newMockDB(t)
		repo := NewEngineerRepository(mock)

		dbErr := errors.New("connection reset")
		mock.ExpectQuery(exact(engineerGetByPhoneNumberSQL)).
			WithArgs("+15555550123").
			WillReturnError(dbErr)

		_, err := repo.GetByPhoneNumber(context.Background(), "+15555550123")
		require.Error(t, err)
		assert.ErrorIs(t, err, dbErr)
		assert.NotErrorIs(t, err, domain.ErrNotFound)
		assert.NoError(t, mock.ExpectationsWereMet())
	})

	t.Run("scan error", func(t *testing.T) {
		mock := newMockDB(t)
		repo := NewEngineerRepository(mock)

		// Wrong number of columns triggers a scan error inside pgxmock.
		rows := mock.NewRows([]string{"id"}).AddRow(int64(7))
		mock.ExpectQuery(exact(engineerGetByPhoneNumberSQL)).
			WithArgs("+15555550123").
			WillReturnRows(rows)

		_, err := repo.GetByPhoneNumber(context.Background(), "+15555550123")
		require.Error(t, err)
		assert.NoError(t, mock.ExpectationsWereMet())
	})
}
