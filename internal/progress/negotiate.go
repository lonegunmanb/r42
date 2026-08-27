package progress

import (
	"bufio"
	"encoding/json"
	"fmt"
	"io"
	"reflect"
	"time"
)

// defaultNegotiationTimeout is the fixed pre-Plan budget for the handshake.
const defaultNegotiationTimeout = 5 * time.Second

type negotiationConfig struct {
	timeout time.Duration
	after   func(time.Duration) <-chan time.Time
}

// NegotiationOption customizes one Negotiate invocation. Tests use it to
// inject deterministic time; production uses the default fixed timeout.
type NegotiationOption func(*negotiationConfig)

// WithNegotiationTimeout overrides the fixed pre-Plan handshake timeout.
func WithNegotiationTimeout(timeout time.Duration) NegotiationOption {
	return func(config *negotiationConfig) { config.timeout = timeout }
}

// WithNegotiationAfter overrides the timer constructor used for the timeout,
// so tests can fire the deadline deterministically without sleeping.
func WithNegotiationAfter(after func(time.Duration) <-chan time.Time) NegotiationOption {
	return func(config *negotiationConfig) { config.after = after }
}

// Negotiate performs the stdin/stdout handshake. It writes and flushes hello,
// reads exactly one select frame from stdin within the timeout, validates the
// worker's selection against the advertised schema majors, writes and flushes
// ready, and returns the encoder for the negotiated major. Negotiation failure
// aborts before Plan or Apply starts, and stdin is not read again afterwards.
func Negotiate(input io.ReadCloser, output io.Writer, options ...NegotiationOption) (*Encoder, error) {
	if nilInput(input) {
		return nil, fmt.Errorf("progress input is required")
	}
	config := negotiationConfig{
		timeout: defaultNegotiationTimeout,
		after:   time.After,
	}
	for _, apply := range options {
		if apply != nil {
			apply(&config)
		}
	}
	helloEncoder, err := NewEncoder(output, SchemaMajor1)
	if err != nil {
		return nil, err
	}
	if err = helloEncoder.EncodeFrame(NewHelloFrame()); err != nil {
		return nil, fmt.Errorf("write hello: %w", err)
	}
	if err = flush(output); err != nil {
		return nil, fmt.Errorf("flush hello: %w", err)
	}
	line, err := readSelectLine(input, config.timeout, config.after)
	if err != nil {
		return nil, err
	}
	selected, err := decodeSelect(line)
	if err != nil {
		return nil, err
	}
	encoder, err := NewEncoder(output, selected)
	if err != nil {
		return nil, fmt.Errorf("negotiate schema %d: %w", selected, err)
	}
	if err = encoder.EncodeFrame(NewReadyFrame(selected)); err != nil {
		return nil, fmt.Errorf("write ready: %w", err)
	}
	if err = flush(output); err != nil {
		return nil, fmt.Errorf("flush ready: %w", err)
	}
	return encoder, nil
}

func nilInput(input io.ReadCloser) bool {
	if input == nil {
		return true
	}
	value := reflect.ValueOf(input)
	switch value.Kind() {
	case reflect.Chan, reflect.Func, reflect.Interface, reflect.Map, reflect.Pointer, reflect.Slice:
		return value.IsNil()
	default:
		return false
	}
}

func flush(output io.Writer) error {
	flusher, ok := output.(interface{ Flush() error })
	if !ok {
		return nil
	}
	return flusher.Flush()
}

type lineResult struct {
	line string
	err  error
}

func readSelectLine(
	input io.ReadCloser,
	timeout time.Duration,
	after func(time.Duration) <-chan time.Time,
) (string, error) {
	result := make(chan lineResult, 1)
	go func() {
		line, err := bufio.NewReader(input).ReadString('\n')
		result <- lineResult{line: line, err: err}
	}()
	select {
	case res := <-result:
		if res.err != nil {
			return "", fmt.Errorf("read select: %w", res.err)
		}
		return res.line, nil
	case <-after(timeout):
		if err := input.Close(); err != nil {
			return "", fmt.Errorf("select timeout after %s: close input: %w", timeout, err)
		}
		return "", fmt.Errorf("select timeout after %s", timeout)
	}
}

func decodeSelect(line string) (int, error) {
	var raw struct {
		Type             string `json:"type"`
		HandshakeVersion int    `json:"handshake_version"`
		SchemaVersion    int    `json:"schema_version"`
	}
	if err := json.Unmarshal([]byte(line), &raw); err != nil {
		return 0, fmt.Errorf("malformed select: %w", err)
	}
	if raw.Type != "select" {
		return 0, fmt.Errorf("expected select frame, got %q", raw.Type)
	}
	selected := SelectFrame{HandshakeVersion: raw.HandshakeVersion, SchemaVersion: raw.SchemaVersion}
	if err := selected.Validate(); err != nil {
		return 0, fmt.Errorf("invalid select: %w", err)
	}
	return selected.SchemaVersion, nil
}
