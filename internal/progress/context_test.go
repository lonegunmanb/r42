package progress_test

import (
	"context"
	"testing"

	"github.com/lonegunmanb/r42/internal/progress"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestEncoderContextRoundTrip(t *testing.T) {
	t.Parallel()

	encoder, err := progress.NewEncoder(new(writeNop), progress.SchemaMajor1)
	require.NoError(t, err)

	ctx := progress.WithEncoder(context.Background(), encoder)

	assert.Same(t, encoder, progress.EncoderFromContext(ctx))
}

func TestEncoderContextAbsentReturnsNil(t *testing.T) {
	t.Parallel()

	assert.Nil(t, progress.EncoderFromContext(context.Background()))
}

// writeNop discards encoded bytes; it exists only to construct an Encoder.
type writeNop struct{}

func (writeNop) Write(payload []byte) (int, error) { return len(payload), nil }
