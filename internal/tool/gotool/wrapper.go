package gotool

func wrapperSource(source string) string {
	return `package main

import (
	_r42context "context"
	_r42encodingjson "encoding/json"
	_r42errors "errors"
	_r42fmt "fmt"
	_r42io "io"
	_r42os "os"
	_r42strings "strings"
)

` + source + injectedTypes + `
func _r42ValidateResponse[T any](response ToolResponse[T]) error {
	if response.Accepted && len(response.Issues) != 0 {
		return _r42fmt.Errorf("accepted response must not contain issues")
	}
	if !response.Accepted && response.Output != nil {
		return _r42fmt.Errorf("rejected response must not contain output")
	}
	if !response.Accepted && len(response.Issues) == 0 {
		return _r42fmt.Errorf("rejected response must contain at least one issue")
	}
	for index, issue := range response.Issues {
		if _r42strings.TrimSpace(issue.Code) == "" {
			return _r42fmt.Errorf("issue %d code is required", index)
		}
		if _r42strings.TrimSpace(issue.Message) == "" {
			return _r42fmt.Errorf("issue %d message is required", index)
		}
	}
	return nil
}

func _r42Fail(err error) {
	_, _ = _r42fmt.Fprintln(_r42os.Stderr, err)
	_r42os.Exit(1)
}

func main() {
	decoder := _r42encodingjson.NewDecoder(_r42os.Stdin)
	decoder.DisallowUnknownFields()
	var input Input
	if err := decoder.Decode(&input); err != nil {
		_r42Fail(_r42fmt.Errorf("decoding input: %w", err))
	}
	var extra any
	if err := decoder.Decode(&extra); !_r42errors.Is(err, _r42io.EOF) {
		_r42Fail(_r42fmt.Errorf("input must contain exactly one JSON value"))
	}
	response, err := Invoke(_r42context.Background(), input)
	if err != nil {
		_r42Fail(err)
	}
	if err = _r42ValidateResponse(response); err != nil {
		_r42Fail(err)
	}
	if err = _r42encodingjson.NewEncoder(_r42os.Stdout).Encode(response); err != nil {
		_r42Fail(_r42fmt.Errorf("encoding response: %w", err))
	}
}
`
}
