package postgres

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestNewPool_InvalidDatabaseURL(t *testing.T) {
	_, err := NewPool(context.Background(), "not-a-valid-connection-string://")
	require.Error(t, err)
}

func TestNewPool_PingFailsWithCanceledContext(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel() // already canceled: Ping fails immediately, and the retry loop's
	// ctx.Done() branch fires without ever sleeping or touching the network.

	_, err := NewPool(ctx, "postgres://user:pass@127.0.0.1:5432/db?sslmode=disable")
	require.Error(t, err)
	assert.ErrorIs(t, err, context.Canceled)
}
