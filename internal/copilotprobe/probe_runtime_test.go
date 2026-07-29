package copilotprobe_test

import (
	"bufio"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	copilot "github.com/github/copilot-sdk/go"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

type recordedRequest struct {
	Method string
	Params map[string]any
}

type probeRuntime struct {
	listener net.Listener
	requests chan recordedRequest
	done     chan struct{}
	errors   chan error
	close    sync.Once
}

func newProbeRuntime(t *testing.T) *probeRuntime {
	t.Helper()

	listener, err := net.Listen("tcp", "127.0.0.1:0")
	require.NoError(t, err)
	runtime := &probeRuntime{
		listener: listener,
		requests: make(chan recordedRequest, 32),
		done:     make(chan struct{}),
		errors:   make(chan error, 1),
	}
	go runtime.serve()
	t.Cleanup(func() {
		runtime.stop(t)
	})
	return runtime
}

func newProbeClient(t *testing.T, runtime *probeRuntime) *copilot.Client {
	t.Helper()

	client := copilot.NewClient(&copilot.ClientOptions{
		Connection: copilot.URIConnection{URL: runtime.listener.Addr().String()},
	})
	t.Cleanup(func() {
		assert.NoError(t, client.Stop())
	})
	return client
}

func (r *probeRuntime) nextRequest(t *testing.T, method string) recordedRequest {
	t.Helper()

	timer := time.NewTimer(5 * time.Second)
	defer timer.Stop()
	for {
		select {
		case request := <-r.requests:
			if request.Method == method {
				return request
			}
		case err := <-r.errors:
			require.NoError(t, err)
		case <-timer.C:
			require.FailNow(t, "request timeout", "did not receive %s", method)
		}
	}
}

func (r *probeRuntime) stop(t *testing.T) {
	t.Helper()

	r.close.Do(func() {
		require.NoError(t, r.listener.Close())
		select {
		case <-r.done:
		case <-time.After(5 * time.Second):
			require.FailNow(t, "runtime shutdown timeout")
		}
	})
}

func (r *probeRuntime) serve() {
	defer close(r.done)

	connection, err := r.listener.Accept()
	if err != nil {
		if !errors.Is(err, net.ErrClosed) {
			r.report(err)
		}
		return
	}
	defer func() {
		_ = connection.Close()
	}()

	reader := bufio.NewReader(connection)
	messageCount := 0
	for {
		frame, readErr := readFrame(reader)
		if readErr != nil {
			if !errors.Is(readErr, io.EOF) {
				r.report(readErr)
			}
			return
		}

		var request struct {
			ID     json.RawMessage `json:"id"`
			Method string          `json:"method"`
			Params map[string]any  `json:"params"`
		}
		if unmarshalErr := json.Unmarshal(frame, &request); unmarshalErr != nil {
			r.report(unmarshalErr)
			return
		}
		r.requests <- recordedRequest{Method: request.Method, Params: request.Params}

		result := map[string]any{}
		switch request.Method {
		case "connect":
			result["protocolVersion"] = copilot.GetSDKProtocolVersion()
		case "session.create", "session.resume":
			sessionID, _ := request.Params["sessionId"].(string)
			if sessionID == "" {
				sessionID = "probe-session"
			}
			result["sessionId"] = sessionID
			result["workspacePath"] = nil
		case "session.send":
			messageCount++
			result["messageId"] = fmt.Sprintf("message-%d", messageCount)
		case "session.options.update":
			result["success"] = true
		case "session.destroy":
		default:
			r.report(fmt.Errorf("unexpected rpc method %q", request.Method))
			return
		}

		response, marshalErr := json.Marshal(map[string]any{
			"jsonrpc": "2.0",
			"id":      request.ID,
			"result":  result,
		})
		if marshalErr != nil {
			r.report(marshalErr)
			return
		}
		if _, writeErr := fmt.Fprintf(connection, "Content-Length: %d\r\n\r\n%s", len(response), response); writeErr != nil {
			return
		}
	}
}

func (r *probeRuntime) report(err error) {
	select {
	case r.errors <- err:
	default:
	}
}

func readFrame(reader *bufio.Reader) ([]byte, error) {
	contentLength := 0
	for {
		line, err := reader.ReadString('\n')
		if err != nil {
			return nil, err
		}
		line = strings.TrimRight(line, "\r\n")
		if line == "" {
			break
		}
		name, value, found := strings.Cut(line, ":")
		if found && strings.EqualFold(strings.TrimSpace(name), "Content-Length") {
			contentLength, err = strconv.Atoi(strings.TrimSpace(value))
			if err != nil {
				return nil, fmt.Errorf("parse content length: %w", err)
			}
		}
	}
	if contentLength <= 0 {
		return nil, fmt.Errorf("missing content length")
	}

	frame := make([]byte, contentLength)
	if _, err := io.ReadFull(reader, frame); err != nil {
		return nil, err
	}
	return frame, nil
}
