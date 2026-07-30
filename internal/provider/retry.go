package provider

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"regexp"
	"syscall"
	"time"
)

const (
	retryMultiplier = 1.5
	retryJitter     = 0.5
)

type RetryOverride struct {
	LifecycleRetries  *int
	ModelCallRetries  *int
	Interval          *time.Duration
	MaxInterval       *time.Duration
	ErrorMessageRegex []string
}

type RetryPolicy struct {
	LifecycleRetries  int
	ModelCallRetries  int
	Interval          time.Duration
	MaxInterval       time.Duration
	Multiplier        float64
	Jitter            float64
	ErrorMessageRegex []string
	compiledRegex     []*regexp.Regexp
}

func (o RetryOverride) Validate() error {
	if o.LifecycleRetries != nil && *o.LifecycleRetries < 0 {
		return errors.New("lifecycle retries must not be negative")
	}
	if o.ModelCallRetries != nil && *o.ModelCallRetries < 0 {
		return errors.New("model call retries must not be negative")
	}
	if o.Interval != nil && *o.Interval < 0 {
		return errors.New("retry interval must not be negative")
	}
	if o.MaxInterval != nil && *o.MaxInterval < 0 {
		return errors.New("maximum retry interval must not be negative")
	}
	if o.Interval != nil && o.MaxInterval != nil && *o.MaxInterval < *o.Interval {
		return errors.New("maximum retry interval must not be less than retry interval")
	}
	for _, expression := range o.ErrorMessageRegex {
		if _, err := regexp.Compile(expression); err != nil {
			return fmt.Errorf("compile error message regex %q: %w", expression, err)
		}
	}
	return nil
}

func DefaultRetryPolicy() RetryPolicy {
	return RetryPolicy{
		LifecycleRetries:  10,
		ModelCallRetries:  5,
		Interval:          10 * time.Second,
		MaxInterval:       180 * time.Second,
		Multiplier:        retryMultiplier,
		Jitter:            retryJitter,
		ErrorMessageRegex: []string{},
		compiledRegex:     []*regexp.Regexp{},
	}
}

func MergeRetry(base RetryPolicy, override RetryOverride) (RetryPolicy, error) {
	if err := override.Validate(); err != nil {
		return RetryPolicy{}, err
	}
	result := base
	result.Multiplier = retryMultiplier
	result.Jitter = retryJitter
	result.ErrorMessageRegex = append([]string{}, base.ErrorMessageRegex...)
	result.ErrorMessageRegex = append(result.ErrorMessageRegex, override.ErrorMessageRegex...)

	if override.LifecycleRetries != nil {
		result.LifecycleRetries = *override.LifecycleRetries
	}
	if override.ModelCallRetries != nil {
		result.ModelCallRetries = *override.ModelCallRetries
	}
	if override.Interval != nil {
		result.Interval = *override.Interval
	}
	if override.MaxInterval != nil {
		result.MaxInterval = *override.MaxInterval
	}
	if err := result.validate(); err != nil {
		return RetryPolicy{}, err
	}

	result.compiledRegex = make([]*regexp.Regexp, 0, len(result.ErrorMessageRegex))
	for _, expression := range result.ErrorMessageRegex {
		compiled, err := regexp.Compile(expression)
		if err != nil {
			return RetryPolicy{}, fmt.Errorf("compile error message regex %q: %w", expression, err)
		}
		result.compiledRegex = append(result.compiledRegex, compiled)
	}
	return result, nil
}

func (p RetryPolicy) validate() error {
	if p.LifecycleRetries < 0 {
		return errors.New("lifecycle retries must not be negative")
	}
	if p.ModelCallRetries < 0 {
		return errors.New("model call retries must not be negative")
	}
	if p.Interval < 0 {
		return errors.New("retry interval must not be negative")
	}
	if p.MaxInterval < 0 {
		return errors.New("maximum retry interval must not be negative")
	}
	if p.MaxInterval < p.Interval {
		return errors.New("maximum retry interval must not be less than retry interval")
	}
	return nil
}

func (p RetryPolicy) Backoff(retryIndex int, random float64) time.Duration {
	base := p.Interval
	for range retryIndex {
		next := time.Duration(float64(base) * p.Multiplier)
		if next >= p.MaxInterval {
			base = p.MaxInterval
			break
		}
		base = next
	}
	delay := time.Duration(float64(base) * (1 - p.Jitter + random))
	if delay > p.MaxInterval {
		return p.MaxInterval
	}
	return delay
}

func Delay(ctx context.Context, delay time.Duration) error {
	select {
	case <-ctx.Done():
		return ctx.Err()
	default:
	}
	if delay <= 0 {
		return nil
	}

	timer := time.NewTimer(delay)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-timer.C:
		return nil
	}
}

type HTTPError struct {
	StatusCode int
	Err        error
}

func (e HTTPError) Error() string {
	if e.Err == nil {
		return fmt.Sprintf("http status %d", e.StatusCode)
	}
	return fmt.Sprintf("http status %d: %v", e.StatusCode, e.Err)
}

func (e HTTPError) Unwrap() error {
	return e.Err
}

func (e HTTPError) HTTPStatusCode() int {
	return e.StatusCode
}

type TransientError struct {
	Err error
}

func (e TransientError) Error() string {
	if e.Err == nil {
		return "transient error"
	}
	return e.Err.Error()
}

func (e TransientError) Unwrap() error {
	return e.Err
}

func (e TransientError) IsTransient() bool {
	return true
}

type PermanentError struct {
	Err error
}

func (e PermanentError) Error() string {
	if e.Err == nil {
		return "permanent error"
	}
	return e.Err.Error()
}

func (e PermanentError) Unwrap() error {
	return e.Err
}

func (e PermanentError) IsPermanent() bool {
	return true
}

func (p RetryPolicy) IsTransient(err error) bool {
	if err == nil || errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
		return false
	}

	var permanent interface{ IsPermanent() bool }
	if errors.As(err, &permanent) && permanent.IsPermanent() {
		return false
	}

	var statusError interface{ HTTPStatusCode() int }
	if errors.As(err, &statusError) {
		status := statusError.HTTPStatusCode()
		if status == 400 || status == 401 || status == 403 {
			return false
		}
		if status == 408 || status == 409 || status == 425 || status == 429 || status >= 500 && status <= 599 {
			return true
		}
	}

	var transient interface{ IsTransient() bool }
	if errors.As(err, &transient) && transient.IsTransient() {
		return true
	}
	var networkError net.Error
	if errors.As(err, &networkError) && networkError.Timeout() {
		return true
	}
	var temporary interface{ Temporary() bool }
	if errors.As(err, &temporary) && temporary.Temporary() {
		return true
	}
	if errors.Is(err, syscall.ECONNRESET) || errors.Is(err, syscall.ECONNREFUSED) {
		return true
	}
	if errors.Is(err, io.EOF) || errors.Is(err, io.ErrUnexpectedEOF) {
		return true
	}
	for _, expression := range p.compiledRegex {
		if expression.MatchString(err.Error()) {
			return true
		}
	}
	return false
}
