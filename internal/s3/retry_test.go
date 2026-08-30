package s3_test

import (
	"context"
	"errors"
	"net"
	"testing"
	"time"

	"github.com/aws/aws-sdk-go/aws/awserr"
	internals3 "github.com/lonegunmanb/r42/internal/s3"
	s3spec "github.com/lonegunmanb/r42/internal/s3/spec"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestRetryAttemptsConfiguredAdditionalRetriesAndBoundsBackoff(t *testing.T) {
	t.Parallel()
	policy, err := internals3.MergeRetry(internals3.RetryPolicy{MaxRetries: 3, Interval: time.Second, MaxInterval: 2 * time.Second}, s3spec.RetryOverride{})
	require.NoError(t, err)
	attempts := 0
	var delays []time.Duration
	err = internals3.RetryWithDelay(t.Context(), policy, func(context.Context) error {
		attempts++
		return timeoutError{}
	}, func(_ context.Context, delay time.Duration) error {
		delays = append(delays, delay)
		return nil
	})
	require.Error(t, err)
	assert.Equal(t, 4, attempts)
	assert.Equal(t, []time.Duration{time.Second, 2 * time.Second, 2 * time.Second}, delays)
}

func TestRetryStopsForPermanentErrorsAndCancellation(t *testing.T) {
	t.Parallel()
	policy := internals3.DefaultRetryPolicy()
	t.Run("permanent", func(t *testing.T) {
		t.Parallel()
		attempts := 0
		err := internals3.RetryWithDelay(t.Context(), policy, func(context.Context) error {
			attempts++
			return awserr.NewRequestFailure(awserr.New("Denied", "forbidden", nil), 403, "request")
		}, func(context.Context, time.Duration) error { t.Fatal("delay must not run"); return nil })
		require.Error(t, err)
		assert.Equal(t, 1, attempts)
	})
	t.Run("canceled", func(t *testing.T) {
		t.Parallel()
		ctx, cancel := context.WithCancel(t.Context())
		attempts := 0
		err := internals3.RetryWithDelay(ctx, policy, func(context.Context) error {
			attempts++
			cancel()
			return timeoutError{}
		}, func(context.Context, time.Duration) error { return context.Canceled })
		require.ErrorIs(t, err, context.Canceled)
		assert.Equal(t, 1, attempts)
	})
}

func TestIsTransientClassifiesS3AndNetworkErrors(t *testing.T) {
	t.Parallel()
	policy := internals3.DefaultRetryPolicy()
	tests := []struct {
		name string
		err  error
		want bool
	}{
		{name: "timeout", err: timeoutError{}, want: true},
		{name: "too many requests", err: awserr.NewRequestFailure(awserr.New("SlowDown", "slow", nil), 429, "request"), want: true},
		{name: "server error", err: awserr.NewRequestFailure(awserr.New("InternalError", "server", nil), 503, "request"), want: true},
		{name: "not found", err: awserr.NewRequestFailure(awserr.New("NoSuchBucket", "missing", nil), 404, "request"), want: false},
		{name: "canceled", err: context.Canceled, want: false},
		{name: "plain", err: errors.New("bad request"), want: false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			assert.Equal(t, tt.want, policy.IsTransient(tt.err))
		})
	}
}

type timeoutError struct{}

func (timeoutError) Error() string   { return "timeout" }
func (timeoutError) Timeout() bool   { return true }
func (timeoutError) Temporary() bool { return true }

var _ net.Error = timeoutError{}
