package postgres

import (
	"context"
	"errors"
	"fmt"

	"github.com/jackc/pgx/v5"

	"github.com/jwke21/rca-transcriber/internal/core/domain"
	"github.com/jwke21/rca-transcriber/internal/core/port"
)

var _ port.EngineerRepository = (*EngineerRepository)(nil)

const engineerGetByPhoneNumberSQL = `SELECT id, phone_number FROM engineers WHERE phone_number = $1`

// EngineerRepository implements port.EngineerRepository on top of Postgres.
type EngineerRepository struct {
	db DB
}

// NewEngineerRepository constructs an EngineerRepository.
func NewEngineerRepository(db DB) *EngineerRepository {
	return &EngineerRepository{db: db}
}

// GetByPhoneNumber returns domain.ErrNotFound if no engineer has this number.
func (r *EngineerRepository) GetByPhoneNumber(ctx context.Context, phoneNumber string) (domain.Engineer, error) {
	row := r.db.QueryRow(ctx, engineerGetByPhoneNumberSQL, phoneNumber)

	var e domain.Engineer
	if err := row.Scan(&e.ID, &e.PhoneNumber); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return domain.Engineer{}, domain.ErrNotFound
		}
		return domain.Engineer{}, fmt.Errorf("postgres: get engineer by phone number: %w", err)
	}

	return e, nil
}
