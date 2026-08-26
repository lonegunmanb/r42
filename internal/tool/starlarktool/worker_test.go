package starlarktool

import (
	"bytes"
	"context"
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestServeEvaluatesOneRequestWithoutSourceFiles(t *testing.T) {
	t.Parallel()
	input := bytes.NewBufferString(`{"code":"result = data + 1","data_json":"2","config":{"max_steps":1000}}`)
	var output bytes.Buffer

	err := Serve(context.Background(), input, &output)

	require.NoError(t, err)
	var response WorkerResponse
	require.NoError(t, json.Unmarshal(output.Bytes(), &response))
	require.Nil(t, response.Error)
	require.NotNil(t, response.Result)
	assert.Equal(t, "3", response.Result.ResultJSON)
}

func TestServeReturnsEvaluatorFailureInResponse(t *testing.T) {
	t.Parallel()
	input := bytes.NewBufferString(`{"code":"result = (","data_json":"null"}`)
	var output bytes.Buffer

	err := Serve(context.Background(), input, &output)

	require.NoError(t, err)
	var response WorkerResponse
	require.NoError(t, json.Unmarshal(output.Bytes(), &response))
	require.Nil(t, response.Result)
	require.NotNil(t, response.Error)
	assert.Equal(t, "starlark_parse_error", response.Error.Code)
	assert.Contains(t, response.Error.Message, "calculator.star")
}

func TestServeRejectsMultipleRequests(t *testing.T) {
	t.Parallel()
	input := bytes.NewBufferString(`{"code":"result = 1","data_json":"null"} {"code":"result = 2","data_json":"null"}`)

	err := Serve(context.Background(), input, new(bytes.Buffer))

	require.Error(t, err)
	assert.ErrorContains(t, err, "expected exactly one JSON value")
}
