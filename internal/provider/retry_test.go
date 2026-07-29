package provider_test

import (
	"context"
	"testing"
	"time"

	"github.com/lonegunmanb/r42/internal/provider"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestDefaultRetryPolicy(t *testing.T) {
	t.Parallel()

	policy := provider.DefaultRetryPolicy()
	assert.Equal(t, 10, policy.LifecycleRetries)
	assert.Equal(t, 5, policy.ModelCallRetries)
	assert.Equal(t, 10*time.Second, policy.Interval)
	assert.Equal(t, 180*time.Second, policy.MaxInterval)
	assert.InDelta(t, 1.5, policy.Multiplier, 0)
	assert.InDelta(t, 0.5, policy.Jitter, 0)
	assert.Empty(t, policy.ErrorMessageRegex)
}

func TestMergeRetryPolicy(t *testing.T) {
	t.Parallel()

	base, err := provider.MergeRetry(provider.DefaultRetryPolicy(), provider.RetryOverride{
		ErrorMessageRegex: []string{"base temporary"},
	})
	require.NoError(t, err)
	actual, err := provider.MergeRetry(base, provider.RetryOverride{
		LifecycleRetries:  pointer(0),
		ModelCallRetries:  pointer(2),
		Interval:          pointer(20 * time.Second),
		MaxInterval:       pointer(60 * time.Second),
		ErrorMessageRegex: []string{"extra temporary"},
	})
	require.NoError(t, err)

	assert.Equal(t, 0, actual.LifecycleRetries)
	assert.Equal(t, 2, actual.ModelCallRetries)
	assert.Equal(t, 20*time.Second, actual.Interval)
	assert.Equal(t, 60*time.Second, actual.MaxInterval)
	assert.InDelta(t, 1.5, actual.Multiplier, 0)
	assert.InDelta(t, 0.5, actual.Jitter, 0)
	assert.Equal(t, []string{"base temporary", "extra temporary"}, actual.ErrorMessageRegex)
}

func TestMergeRetryPolicyRejectsInvalidValues(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name          string
		override      provider.RetryOverride
		expectedError string
	}{
		{name: "negative lifecycle retries", override: provider.RetryOverride{LifecycleRetries: pointer(-1)}, expectedError: "lifecycle retries must not be negative"},
		{name: "negative model retries", override: provider.RetryOverride{ModelCallRetries: pointer(-1)}, expectedError: "model call retries must not be negative"},
		{name: "negative interval", override: provider.RetryOverride{Interval: pointer(-time.Second)}, expectedError: "retry interval must not be negative"},
		{name: "negative max interval", override: provider.RetryOverride{MaxInterval: pointer(-time.Second)}, expectedError: "maximum retry interval must not be negative"},
		{name: "max below interval", override: provider.RetryOverride{Interval: pointer(20 * time.Second), MaxInterval: pointer(10 * time.Second)}, expectedError: "maximum retry interval must not be less than retry interval"},
		{name: "invalid regex", override: provider.RetryOverride{ErrorMessageRegex: []string{"["}}, expectedError: "compile error message regex \"[\": error parsing regexp: missing closing ]: `[`"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			_, err := provider.MergeRetry(provider.DefaultRetryPolicy(), tt.override)
			assert.EqualError(t, err, tt.expectedError)
		})
	}
}

func TestRetryPolicyBackoff(t *testing.T) {
	t.Parallel()

	policy := provider.DefaultRetryPolicy()
	tests := []struct {
		name       string
		retryIndex int
		random     float64
		expected   time.Duration
	}{
		{name: "lower jitter bound", retryIndex: 0, random: 0, expected: 5 * time.Second},
		{name: "midpoint", retryIndex: 0, random: 0.5, expected: 10 * time.Second},
		{name: "upper jitter bound", retryIndex: 0, random: 1, expected: 15 * time.Second},
		{name: "exponential", retryIndex: 2, random: 0.5, expected: 22500 * time.Millisecond},
		{name: "capped", retryIndex: 20, random: 1, expected: 180 * time.Second},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			assert.Equal(t, tt.expected, policy.Backoff(tt.retryIndex, tt.random))
		})
	}
}

func TestDelayRespectsCancellation(t *testing.T) {
	t.Parallel()

	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	require.ErrorIs(t, provider.Delay(ctx, time.Hour), context.Canceled)
	require.ErrorIs(t, provider.Delay(ctx, 0), context.Canceled)
	assert.NoError(t, provider.Delay(t.Context(), 0))
}

func TestDelayCompletesAndCancelsWhileWaiting(t *testing.T) {
	t.Parallel()

	require.NoError(t, provider.Delay(t.Context(), time.Millisecond))

	ctx, cancel := context.WithCancel(t.Context())
	t.Cleanup(cancel)
	time.AfterFunc(time.Millisecond, cancel)
	require.ErrorIs(t, provider.Delay(ctx, time.Hour), context.Canceled)
}

func TestMergeRetryPolicyCopiesAndRecompilesBaseRegex(t *testing.T) {
	t.Parallel()

	base := provider.DefaultRetryPolicy()
	base.ErrorMessageRegex = []string{"general error"}
	merged, err := provider.MergeRetry(base, provider.RetryOverride{})
	require.NoError(t, err)

	base.ErrorMessageRegex[0] = "changed"
	assert.Equal(t, []string{"general error"}, merged.ErrorMessageRegex)
	assert.True(t, merged.IsTransient(assert.AnError))
}

func TestMergeRetryPolicyRejectsInvalidBaseRegex(t *testing.T) {
	t.Parallel()

	base := provider.DefaultRetryPolicy()
	base.ErrorMessageRegex = []string{"["}
	_, err := provider.MergeRetry(base, provider.RetryOverride{})
	assert.Error(t, err)
}
