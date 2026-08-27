package progress_test

import (
	"bytes"
	"errors"
	"io"
	"strings"
	"testing"
	"time"

	"github.com/lonegunmanb/r42/internal/progress"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestNegotiateSelectsSchemaAndWritesHelloReady(t *testing.T) {
	t.Parallel()

	reader := io.NopCloser(strings.NewReader(`{"type":"select","handshake_version":1,"schema_version":1}` + "\n"))
	var stdout bytes.Buffer
	encoder, err := progress.Negotiate(reader, &stdout, progress.WithNegotiationTimeout(time.Second))
	require.NoError(t, err)
	require.NotNil(t, encoder)

	lines := strings.Split(strings.TrimSuffix(stdout.String(), "\n"), "\n")
	require.Len(t, lines, 2)
	assert.Equal(t, fixture(t, "hello.ndjson"), lines[0]+"\n")
	assert.Equal(t, fixture(t, "ready.ndjson"), lines[1]+"\n")
}

func TestNegotiateReturnsEncoderForNegotiatedSchema(t *testing.T) {
	t.Parallel()

	reader := io.NopCloser(strings.NewReader(`{"type":"select","handshake_version":1,"schema_version":1}` + "\n"))
	var stdout bytes.Buffer
	encoder, err := progress.Negotiate(reader, &stdout, progress.WithNegotiationTimeout(time.Second))
	require.NoError(t, err)

	// The selected encoder is wired to the negotiated schema major and the
	// protocol stdout, so a post-ready record must carry schema_version 1.
	require.NoError(t, encoder.EncodeRecord(progress.Envelope{RunID: "run-x"}, &progress.NodeRecord{
		Node: progress.NodeProjection{BlockAddress: "a", BlockKind: "research", Status: progress.StatusWaiting},
	}))
	var decoded map[string]any
	lastLine := strings.TrimSuffix(stdout.String(), "\n")
	require.NoError(t, decodeJSON(t, lastLine[strings.LastIndex(lastLine, "\n")+1:], &decoded))
	assert.InDelta(t, 1, decoded["schema_version"], 0)
	assert.Equal(t, "node_upsert", decoded["type"])
}

func TestNegotiateRejectsMalformedInput(t *testing.T) {
	t.Parallel()

	reader := io.NopCloser(strings.NewReader("this is not json\n"))
	_, err := progress.Negotiate(reader, new(bytes.Buffer), progress.WithNegotiationTimeout(time.Second))
	require.Error(t, err)
	assert.ErrorContains(t, err, "select")
}

func TestNegotiateRejectsNonSelectFrame(t *testing.T) {
	t.Parallel()

	reader := io.NopCloser(strings.NewReader(`{"type":"ready","handshake_version":1,"schema_version":1}` + "\n"))
	_, err := progress.Negotiate(reader, new(bytes.Buffer), progress.WithNegotiationTimeout(time.Second))
	require.Error(t, err)
	assert.ErrorContains(t, err, "select")
}

func TestNegotiateRejectsUnsupportedSchema(t *testing.T) {
	t.Parallel()

	reader := io.NopCloser(strings.NewReader(`{"type":"select","handshake_version":1,"schema_version":99}` + "\n"))
	_, err := progress.Negotiate(reader, new(bytes.Buffer), progress.WithNegotiationTimeout(time.Second))
	require.Error(t, err)
	assert.ErrorContains(t, err, "schema_version")
}

func TestNegotiateRejectsWrongHandshakeVersion(t *testing.T) {
	t.Parallel()

	reader := io.NopCloser(strings.NewReader(`{"type":"select","handshake_version":2,"schema_version":1}` + "\n"))
	_, err := progress.Negotiate(reader, new(bytes.Buffer), progress.WithNegotiationTimeout(time.Second))
	require.Error(t, err)
	assert.ErrorContains(t, err, "handshake_version")
}

func TestNegotiateFailsOnEOF(t *testing.T) {
	t.Parallel()

	reader := io.NopCloser(strings.NewReader(""))
	_, err := progress.Negotiate(reader, new(bytes.Buffer), progress.WithNegotiationTimeout(time.Second))
	require.Error(t, err)
	assert.ErrorContains(t, err, "EOF")
}

func TestNegotiateRejectsNilInputWithoutWritingHello(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name  string
		input io.ReadCloser
	}{
		{name: "nil interface"},
		{name: "typed nil", input: (*closeTrackingReader)(nil)},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			var stdout bytes.Buffer
			var err error
			require.NotPanics(t, func() {
				_, err = progress.Negotiate(test.input, &stdout, progress.WithNegotiationTimeout(time.Second))
			})
			require.ErrorContains(t, err, "input")
			assert.Empty(t, stdout.String())
		})
	}
}

func TestNegotiateTimesOutWithoutSelect(t *testing.T) {
	t.Parallel()

	reader, writer := io.Pipe()
	// No select is written; the internal reader goroutine blocks on the pipe.
	// Pre-close the injected deadline channel so the select fires immediately.
	fired := make(chan time.Time)
	close(fired)
	after := func(time.Duration) <-chan time.Time { return fired }
	var stdout bytes.Buffer
	_, err := progress.Negotiate(reader, &stdout,
		progress.WithNegotiationTimeout(time.Second),
		progress.WithNegotiationAfter(after),
	)
	require.Error(t, err)
	require.ErrorContains(t, err, "timeout")
	// Closing the writer unblocks the internal reader goroutine with io.EOF.
	require.NoError(t, writer.Close())
}

func TestNegotiateTimesOutUsingFixedDefaultWithoutSelect(t *testing.T) {
	t.Parallel()

	// The production path uses the fixed 5-second pre-Plan timeout. Fire the
	// default deadline deterministically without sleeping.
	reader, writer := io.Pipe()
	fired := make(chan time.Time)
	close(fired)
	after := func(time.Duration) <-chan time.Time { return fired }
	var stdout bytes.Buffer
	_, err := progress.Negotiate(reader, &stdout, progress.WithNegotiationAfter(after))
	require.Error(t, err)
	require.ErrorContains(t, err, "5s")
	require.NoError(t, writer.Close())
}

func TestNegotiateTimeoutClosesClosableInput(t *testing.T) {
	t.Parallel()

	reader, writer := io.Pipe()
	t.Cleanup(func() { require.NoError(t, writer.Close()) })
	tracked := &closeTrackingReader{ReadCloser: reader, closed: make(chan struct{})}
	fired := make(chan time.Time)
	close(fired)
	_, err := progress.Negotiate(tracked, io.Discard,
		progress.WithNegotiationAfter(func(time.Duration) <-chan time.Time { return fired }),
	)

	require.ErrorContains(t, err, "timeout")
	select {
	case <-tracked.closed:
	default:
		t.Fatal("timeout must close a closable input to stop the pending read")
	}
}

func TestNegotiateFailsWhenHelloCannotBeWritten(t *testing.T) {
	t.Parallel()

	reader := io.NopCloser(strings.NewReader(`{"type":"select","handshake_version":1,"schema_version":1}` + "\n"))
	_, err := progress.Negotiate(reader, &failingWriter{err: errors.New("stdout closed")}, progress.WithNegotiationTimeout(time.Second))
	require.Error(t, err)
	assert.ErrorContains(t, err, "hello")
}

func TestNegotiateFailsWhenHelloCannotBeFlushed(t *testing.T) {
	t.Parallel()

	reader := io.NopCloser(strings.NewReader(`{"type":"select","handshake_version":1,"schema_version":1}` + "\n"))
	writer := &flushFailWriter{failAt: 1, err: errors.New("stdout closed")}
	_, err := progress.Negotiate(reader, writer, progress.WithNegotiationTimeout(time.Second))
	require.Error(t, err)
	assert.ErrorContains(t, err, "flush hello")
}

func TestNegotiateFailsWhenReadyCannotBeFlushed(t *testing.T) {
	t.Parallel()

	reader := io.NopCloser(strings.NewReader(`{"type":"select","handshake_version":1,"schema_version":1}` + "\n"))
	writer := &flushFailWriter{failAt: 2, err: errors.New("stdout closed")}
	_, err := progress.Negotiate(reader, writer, progress.WithNegotiationTimeout(time.Second))
	require.Error(t, err)
	assert.ErrorContains(t, err, "flush ready")
}

type failingWriter struct{ err error }

func (w *failingWriter) Write([]byte) (int, error) { return 0, w.err }

type closeTrackingReader struct {
	io.ReadCloser
	closed chan struct{}
}

func (r *closeTrackingReader) Close() error {
	select {
	case <-r.closed:
	default:
		close(r.closed)
	}
	return r.ReadCloser.Close()
}

type flushFailWriter struct {
	bytes.Buffer
	flushCount int
	failAt     int
	err        error
}

func (w *flushFailWriter) Flush() error {
	w.flushCount++
	if w.flushCount == w.failAt {
		return w.err
	}
	return nil
}
