package s3

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"regexp"
	"syscall"
	"time"

	"github.com/aws/aws-sdk-go/aws/awserr"
	s3spec "github.com/lonegunmanb/r42/internal/s3/spec"
)

type RetryPolicy struct {
	MaxRetries       int
	Interval         time.Duration
	MaxInterval      time.Duration
	compiledMatchers []*regexp.Regexp
}

func DefaultRetryPolicy() RetryPolicy {
	return RetryPolicy{MaxRetries: 3, Interval: time.Second, MaxInterval: 30 * time.Second}
}

func MergeRetry(base RetryPolicy, override s3spec.RetryOverride) (RetryPolicy, error) {
	if err := override.Validate(); err != nil {
		return RetryPolicy{}, err
	}
	result := base
	if override.MaxRetries != nil {
		result.MaxRetries = *override.MaxRetries
	}
	if override.Interval != nil {
		result.Interval = *override.Interval
	}
	if override.MaxInterval != nil {
		result.MaxInterval = *override.MaxInterval
	}
	if result.MaxRetries < 0 || result.Interval < 0 || result.MaxInterval < result.Interval {
		return RetryPolicy{}, errors.New("invalid S3 retry policy")
	}
	result.compiledMatchers = make([]*regexp.Regexp, 0, len(override.ErrorMessageRegex))
	for _, expression := range override.ErrorMessageRegex {
		matcher, err := regexp.Compile(expression)
		if err != nil {
			return RetryPolicy{}, fmt.Errorf("compile S3 retry error matcher %q: %w", expression, err)
		}
		result.compiledMatchers = append(result.compiledMatchers, matcher)
	}
	return result, nil
}

func (p RetryPolicy) IsTransient(err error) bool {
	if err == nil || errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
		return false
	}
	var requestFailure awserr.RequestFailure
	if errors.As(err, &requestFailure) {
		status := requestFailure.StatusCode()
		return status == 408 || status == 429 || status >= 500 && status <= 599
	}
	var networkError net.Error
	if errors.As(err, &networkError) && networkError.Timeout() {
		return true
	}
	if errors.Is(err, io.EOF) || errors.Is(err, io.ErrUnexpectedEOF) || errors.Is(err, syscall.ECONNRESET) || errors.Is(err, syscall.ECONNREFUSED) {
		return true
	}
	for _, matcher := range p.compiledMatchers {
		if matcher.MatchString(err.Error()) {
			return true
		}
	}
	return false
}

func (p RetryPolicy) Backoff(retry int) time.Duration {
	delay := p.Interval
	for range retry {
		if delay >= p.MaxInterval {
			return p.MaxInterval
		}
		delay *= 2
		if delay > p.MaxInterval {
			return p.MaxInterval
		}
	}
	return delay
}

func Retry(ctx context.Context, policy RetryPolicy, operation func(context.Context) error) error {
	return RetryWithDelay(ctx, policy, operation, delay)
}

func RetryWithDelay(ctx context.Context, policy RetryPolicy, operation func(context.Context) error, wait func(context.Context, time.Duration) error) error {
	for attempt := 0; ; attempt++ {
		if err := ctx.Err(); err != nil {
			return err
		}
		err := operation(ctx)
		if err == nil || !policy.IsTransient(err) || attempt >= policy.MaxRetries {
			return err
		}
		if err = wait(ctx, policy.Backoff(attempt)); err != nil {
			return err
		}
	}
}

func delay(ctx context.Context, duration time.Duration) error {
	if duration <= 0 {
		return ctx.Err()
	}
	timer := time.NewTimer(duration)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-timer.C:
		return nil
	}
}
