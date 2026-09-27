package postgres

import (
	"regexp"
	"testing"

	"github.com/pashagolub/pgxmock/v4"
	"github.com/stretchr/testify/require"
)

// newMockDB returns a pgxmock pool that satisfies DB, and registers its
// close and ExpectationsWereMet check for test cleanup.
func newMockDB(t *testing.T) pgxmock.PgxPoolIface {
	t.Helper()
	mock, err := pgxmock.NewPool()
	require.NoError(t, err)
	t.Cleanup(func() {
		mock.Close()
	})
	return mock
}

// exact turns sql into a regexp that matches it exactly (modulo whitespace,
// which pgxmock's default matcher already collapses), so tests don't have to
// worry about "$" and "(" being regexp metacharacters.
func exact(sql string) string {
	return regexp.QuoteMeta(sql)
}
