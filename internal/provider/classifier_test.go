package provider_test

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"syscall"
	"testing"

	"github.com/lonegunmanb/r42/internal/provider"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestRetryPolicyIsTransient(t *testing.T) {
	t.Parallel()

	policy, err := provider.MergeRetry(provider.DefaultRetryPolicy(), provider.RetryOverride{
		ErrorMessageRegex: []string{"temporarily unavailable"},
	})
	require.NoError(t, err)

	tests := []struct {
		name      string
		err       error
		transient bool
	}{
		{name: "http 408", err: provider.HTTPError{StatusCode: 408}, transient: true},
		{name: "http 409", err: provider.HTTPError{StatusCode: 409}, transient: true},
		{name: "http 425", err: provider.HTTPError{StatusCode: 425}, transient: true},
		{name: "http 429", err: provider.HTTPError{StatusCode: 429}, transient: true},
		{name: "http 500", err: provider.HTTPError{StatusCode: 500}, transient: true},
		{name: "http 599", err: provider.HTTPError{StatusCode: 599}, transient: true},
		{name: "http 400", err: provider.HTTPError{StatusCode: 400}},
		{name: "http 401", err: provider.HTTPError{StatusCode: 401}},
		{name: "http 403", err: provider.HTTPError{StatusCode: 403}},
		{name: "other http 4xx", err: provider.HTTPError{StatusCode: 422}},
		{name: "canceled", err: context.Canceled},
		{name: "deadline", err: context.DeadlineExceeded},
		{name: "timeout", err: &net.DNSError{IsTimeout: true}, transient: true},
		{name: "connection reset", err: fmt.Errorf("read: %w", syscall.ECONNRESET), transient: true},
		{name: "connection refused", err: fmt.Errorf("dial: %w", syscall.ECONNREFUSED), transient: true},
		{name: "eof", err: io.EOF, transient: true},
		{name: "unexpected eof", err: io.ErrUnexpectedEOF, transient: true},
		{name: "typed transient sdk error", err: provider.TransientError{Err: errors.New("runtime unavailable")}, transient: true},
		{name: "typed permanent auth error", err: provider.PermanentError{Err: errors.New("authentication failed")}},
		{name: "custom regex", err: errors.New("service temporarily unavailable"), transient: true},
		{
			name: "custom regex cannot override permanent status",
			err: provider.HTTPError{
				StatusCode: 400,
				Err:        errors.New("temporarily unavailable"),
			},
		},
		{
			name: "custom regex extends unclassified http status",
			err: provider.HTTPError{
				StatusCode: 422,
				Err:        errors.New("temporarily unavailable"),
			},
			transient: true,
		},
		{
			name: "permanent wrapper wins",
			err: provider.PermanentError{
				Err: provider.TransientError{Err: errors.New("runtime unavailable")},
			},
		},
		{name: "ordinary", err: errors.New("invalid request")},
		{name: "nil"},
		{name: "temporary", err: temporaryError{}, transient: true},
		{name: "explicit non-permanent", err: classificationError{}},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			assert.Equal(t, tt.transient, policy.IsTransient(tt.err))
		})
	}
}

func TestClassificationErrorWrappersHaveSafeZeroValues(t *testing.T) {
	t.Parallel()

	assert.Equal(t, "transient error", (provider.TransientError{}).Error())
	assert.Equal(t, "permanent error", (provider.PermanentError{}).Error())
	assert.Equal(t, "runtime unavailable", (provider.TransientError{Err: errors.New("runtime unavailable")}).Error())
	assert.Equal(t, "invalid request", (provider.PermanentError{Err: errors.New("invalid request")}).Error())
}

func TestHTTPErrorText(t *testing.T) {
	t.Parallel()

	assert.Equal(t, "http status 503", (provider.HTTPError{StatusCode: 503}).Error())
	assert.Equal(t, "http status 503: unavailable", (provider.HTTPError{
		StatusCode: 503,
		Err:        errors.New("unavailable"),
	}).Error())
}

type temporaryError struct{}

func (temporaryError) Error() string   { return "temporary" }
func (temporaryError) Temporary() bool { return true }

type classificationError struct{}

func (classificationError) Error() string     { return "unclassified" }
func (classificationError) IsPermanent() bool { return false }
