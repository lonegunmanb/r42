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
		{name: "http 408", err: httpError{StatusCode: 408}, transient: true},
		{name: "http 409", err: httpError{StatusCode: 409}, transient: true},
		{name: "http 425", err: httpError{StatusCode: 425}, transient: true},
		{name: "http 429", err: httpError{StatusCode: 429}, transient: true},
		{name: "http 500", err: httpError{StatusCode: 500}, transient: true},
		{name: "http 599", err: httpError{StatusCode: 599}, transient: true},
		{name: "http 400", err: httpError{StatusCode: 400}},
		{name: "http 401", err: httpError{StatusCode: 401}},
		{name: "http 403", err: httpError{StatusCode: 403}},
		{name: "other http 4xx", err: httpError{StatusCode: 422}},
		{name: "canceled", err: context.Canceled},
		{name: "deadline", err: context.DeadlineExceeded},
		{name: "timeout", err: &net.DNSError{IsTimeout: true}, transient: true},
		{name: "connection reset", err: fmt.Errorf("read: %w", syscall.ECONNRESET), transient: true},
		{name: "connection refused", err: fmt.Errorf("dial: %w", syscall.ECONNREFUSED), transient: true},
		{name: "eof", err: io.EOF, transient: true},
		{name: "unexpected eof", err: io.ErrUnexpectedEOF, transient: true},
		{name: "typed transient sdk error", err: transientError{Err: errors.New("runtime unavailable")}, transient: true},
		{name: "typed permanent auth error", err: permanentError{Err: errors.New("authentication failed")}},
		{name: "custom regex", err: errors.New("service temporarily unavailable"), transient: true},
		{
			name: "custom regex cannot override permanent status",
			err: httpError{
				StatusCode: 400,
				Err:        errors.New("temporarily unavailable"),
			},
		},
		{
			name: "custom regex extends unclassified http status",
			err: httpError{
				StatusCode: 422,
				Err:        errors.New("temporarily unavailable"),
			},
			transient: true,
		},
		{
			name: "permanent wrapper wins",
			err: permanentError{
				Err: transientError{Err: errors.New("runtime unavailable")},
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

type temporaryError struct{}

func (temporaryError) Error() string   { return "temporary" }
func (temporaryError) Temporary() bool { return true }

type classificationError struct{}

func (classificationError) Error() string     { return "unclassified" }
func (classificationError) IsPermanent() bool { return false }

type httpError struct {
	StatusCode int
	Err        error
}

func (e httpError) Error() string {
	if e.Err == nil {
		return fmt.Sprintf("http status %d", e.StatusCode)
	}
	return fmt.Sprintf("http status %d: %v", e.StatusCode, e.Err)
}

func (e httpError) Unwrap() error       { return e.Err }
func (e httpError) HTTPStatusCode() int { return e.StatusCode }

type transientError struct{ Err error }

func (e transientError) Error() string   { return e.Err.Error() }
func (e transientError) Unwrap() error   { return e.Err }
func (transientError) IsTransient() bool { return true }

type permanentError struct{ Err error }

func (e permanentError) Error() string   { return e.Err.Error() }
func (e permanentError) Unwrap() error   { return e.Err }
func (permanentError) IsPermanent() bool { return true }
