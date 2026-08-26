// Package starlarktool evaluates isolated numerical Starlark programs.
package starlarktool

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math"
	"math/big"
	"strings"

	starlarkmath "go.starlark.net/lib/math"
	"go.starlark.net/starlark"
	"go.starlark.net/starlarkstruct"
	"go.starlark.net/syntax"
)

const (
	defaultMaxSteps       = 1_000_000
	defaultMaxSourceBytes = 65_536
	defaultMaxDataBytes   = 1_048_576
	defaultMaxResultBytes = 1_048_576
	defaultMaxStdoutBytes = 16_384
)

// Config bounds a single evaluator invocation.
type Config struct {
	MaxSteps       int
	MaxSourceBytes int
	MaxDataBytes   int
	MaxResultBytes int
	MaxStdoutBytes int
}

// DefaultConfig returns the resource defaults declared by starlark_tool.
func DefaultConfig() Config {
	return Config{
		MaxSteps: defaultMaxSteps, MaxSourceBytes: defaultMaxSourceBytes,
		MaxDataBytes: defaultMaxDataBytes, MaxResultBytes: defaultMaxResultBytes,
		MaxStdoutBytes: defaultMaxStdoutBytes,
	}
}

// Result is the fixed successful evaluator response.
type Result struct {
	ResultJSON string
	Stdout     string
	Steps      uint64
}

// Error is a repairable evaluator failure with a stable code.
type Error struct {
	Code    string
	Message string
	Err     error
}

func (e *Error) Error() string {
	if e.Err == nil {
		return e.Message
	}
	return e.Message + ": " + e.Err.Error()
}

func (e *Error) Unwrap() error { return e.Err }

// Evaluate runs code against one JSON data value without filesystem or module loading.
func Evaluate(ctx context.Context, config Config, code, dataJSON string) (Result, error) {
	config = normalizedConfig(config)
	if strings.TrimSpace(code) == "" {
		return Result{}, failure("starlark_code_required", "code is required", nil)
	}
	if len(code) > config.MaxSourceBytes {
		return Result{}, failure("starlark_output_limit", "code exceeds max_source_bytes", nil)
	}
	if len(dataJSON) > config.MaxDataBytes {
		return Result{}, failure("starlark_data_json", "data_json exceeds max_data_bytes", nil)
	}
	data, err := decodeJSON(dataJSON)
	if err != nil {
		return Result{}, failure("starlark_data_json", "data_json must contain one valid JSON value", err)
	}
	data.Freeze()

	var stdout limitedBuffer
	stdout.limit = config.MaxStdoutBytes
	thread := &starlark.Thread{
		Name: "calculator.star",
		Print: func(_ *starlark.Thread, message string) {
			_, _ = stdout.WriteString(message + "\n")
		},
		Load: func(_ *starlark.Thread, module string) (starlark.StringDict, error) {
			return nil, fmt.Errorf("load is unavailable: %s", module)
		},
	}
	done := make(chan struct{})
	go func() {
		select {
		case <-ctx.Done():
			thread.Cancel(ctx.Err().Error())
		case <-done:
		}
	}()
	defer close(done)

	modules, err := bundledModules(thread)
	if err != nil {
		return Result{}, failure("starlark_runtime_error", "initialize numerical modules", err)
	}
	thread.SetMaxExecutionSteps(thread.ExecutionSteps() + uint64(config.MaxSteps))
	globals, err := starlark.ExecFileOptions(&syntax.FileOptions{}, thread, "calculator.star", code, starlark.StringDict{
		"data":   data,
		"math":   starlarkmath.Module,
		"stats":  modules["stats"],
		"matrix": modules["matrix"],
	})
	steps := thread.ExecutionSteps()
	if stdout.exceeded {
		return Result{}, failure("starlark_output_limit", "stdout exceeds max_stdout_bytes", nil)
	}
	if err != nil {
		return Result{}, classifyEvaluationError(err, stdout.String(), steps)
	}
	value, ok := globals["result"]
	if !ok {
		return Result{}, failure("starlark_result_missing", "top-level result is required", nil)
	}
	jsonValue, err := resultJSONValue(value, make(map[starlark.Value]struct{}), 0)
	if err != nil {
		return Result{}, err
	}
	encoded, err := json.Marshal(jsonValue)
	if err != nil {
		return Result{}, failure("starlark_result_type", "result is not JSON-compatible", err)
	}
	if len(encoded) > config.MaxResultBytes {
		return Result{}, failure("starlark_output_limit", "result exceeds max_result_bytes", nil)
	}
	return Result{ResultJSON: string(encoded), Stdout: stdout.String(), Steps: steps}, nil
}

const moduleSource = `
def _numbers(values, name):
  if len(values) == 0:
    fail(name + " requires a non-empty vector")
  for value in values:
    if type(value) != "int" and type(value) != "float":
      fail(name + " accepts only int and float values")
    if type(value) == "float" and not _is_finite(value):
      fail(name + " rejects NaN and infinity")

def _mean(values):
  _numbers(values, "mean")
  total = 0.0
  for value in values:
    total += value
  return total / len(values)

def _variance(values, population):
  _numbers(values, "variance")
  if not population and len(values) < 2:
    fail("sample variance requires at least two observations")
  center = _mean(values)
  total = 0.0
  for value in values:
    delta = value - center
    total += delta * delta
  divisor = len(values) if population else len(values) - 1
  return total / divisor

def _variance_sample(values):
  return _variance(values, False)

def _variance_population(values):
  return _variance(values, True)

def _median(values):
  _numbers(values, "median")
  ordered = sorted(values)
  middle = len(ordered) // 2
  if len(ordered) % 2 == 1:
    return ordered[middle]
  return (ordered[middle - 1] + ordered[middle]) / 2.0

def _stdev(values, population):
  return math.sqrt(_variance(values, population))

def _stdev_sample(values):
  return _stdev(values, False)

def _stdev_population(values):
  return _stdev(values, True)

def _covariance(first, second):
  _numbers(first, "covariance")
  _numbers(second, "covariance")
  if len(first) != len(second):
    fail("covariance vectors must have equal length")
  if len(first) < 2:
    fail("sample covariance requires at least two observations")
  first_mean = _mean(first)
  second_mean = _mean(second)
  total = 0.0
  for index in range(len(first)):
    total += (first[index] - first_mean) * (second[index] - second_mean)
  return total / (len(first) - 1)

def _matrix_shape(matrix):
  if type(matrix) != "list" or len(matrix) == 0:
    fail("matrix must be a non-empty two-dimensional list")
  columns = None
  for row in matrix:
    if type(row) != "list" or len(row) == 0:
      fail("matrix must be a non-empty two-dimensional list")
    if columns == None:
      columns = len(row)
    elif len(row) != columns:
      fail("matrix must not be ragged")
    _numbers(row, "matrix")
  return [len(matrix), columns]

def _transpose(matrix):
  shape = _matrix_shape(matrix)
  result = []
  for column in range(shape[1]):
    row = []
    for index in range(shape[0]):
      row.append(matrix[index][column])
    result.append(row)
  return result

def _matmul(left, right):
  left_shape = _matrix_shape(left)
  right_shape = _matrix_shape(right)
  if left_shape[1] != right_shape[0]:
    fail("matrix dimensions are incompatible for multiplication")
  result = []
  for left_row in range(left_shape[0]):
    row = []
    for right_column in range(right_shape[1]):
      total = 0
      for index in range(left_shape[1]):
        total += left[left_row][index] * right[index][right_column]
      row.append(total)
    result.append(row)
  return result
`

func bundledModules(thread *starlark.Thread) (map[string]starlark.Value, error) {
	functions, err := starlark.ExecFileOptions(&syntax.FileOptions{}, thread, "calculator.star", moduleSource, starlark.StringDict{
		"math":       starlarkmath.Module,
		"_is_finite": starlark.NewBuiltin("_is_finite", isFiniteBuiltin),
	})
	if err != nil {
		return nil, err
	}
	stats := starlark.StringDict{
		"mean": functions["_mean"], "median": functions["_median"],
		"variance": functions["_variance_sample"], "pvariance": functions["_variance_population"],
		"stdev": functions["_stdev_sample"], "pstdev": functions["_stdev_population"],
		"covariance": functions["_covariance"],
	}
	matrix := starlark.StringDict{
		"shape": functions["_matrix_shape"], "transpose": functions["_transpose"], "matmul": functions["_matmul"],
	}
	return map[string]starlark.Value{
		"stats":  starlarkstruct.FromStringDict(starlark.String("stats"), stats),
		"matrix": starlarkstruct.FromStringDict(starlark.String("matrix"), matrix),
	}, nil
}

func normalizedConfig(config Config) Config {
	defaults := DefaultConfig()
	if config.MaxSteps <= 0 {
		config.MaxSteps = defaults.MaxSteps
	}
	if config.MaxSourceBytes <= 0 {
		config.MaxSourceBytes = defaults.MaxSourceBytes
	}
	if config.MaxDataBytes <= 0 {
		config.MaxDataBytes = defaults.MaxDataBytes
	}
	if config.MaxResultBytes <= 0 {
		config.MaxResultBytes = defaults.MaxResultBytes
	}
	if config.MaxStdoutBytes <= 0 {
		config.MaxStdoutBytes = defaults.MaxStdoutBytes
	}
	return config
}

func decodeJSON(source string) (starlark.Value, error) {
	decoder := json.NewDecoder(strings.NewReader(source))
	decoder.UseNumber()
	var value any
	if err := decoder.Decode(&value); err != nil {
		return nil, err
	}
	if err := decoder.Decode(new(any)); !errors.Is(err, io.EOF) {
		return nil, errors.New("multiple JSON values")
	}
	return jsonToStarlark(value)
}

func jsonToStarlark(value any) (starlark.Value, error) {
	switch value := value.(type) {
	case nil:
		return starlark.None, nil
	case bool:
		return starlark.Bool(value), nil
	case string:
		return starlark.String(value), nil
	case json.Number:
		if integer, ok := new(big.Int).SetString(string(value), 10); ok {
			return starlark.MakeBigInt(integer), nil
		}
		float, err := value.Float64()
		if err != nil || !isFinite(float) {
			return nil, fmt.Errorf("invalid JSON number %q", value)
		}
		return starlark.Float(float), nil
	case []any:
		items := make([]starlark.Value, len(value))
		for index, item := range value {
			converted, err := jsonToStarlark(item)
			if err != nil {
				return nil, err
			}
			items[index] = converted
		}
		return starlark.NewList(items), nil
	case map[string]any:
		dictionary := starlark.NewDict(len(value))
		for key, item := range value {
			converted, err := jsonToStarlark(item)
			if err != nil {
				return nil, err
			}
			if err := dictionary.SetKey(starlark.String(key), converted); err != nil {
				return nil, err
			}
		}
		return dictionary, nil
	default:
		return nil, fmt.Errorf("unsupported JSON value %T", value)
	}
}

func resultJSONValue(value starlark.Value, active map[starlark.Value]struct{}, depth int) (any, error) {
	if depth > 1_000 {
		return nil, failure("starlark_result_type", "result nesting is too deep", nil)
	}
	switch value := value.(type) {
	case starlark.NoneType:
		return nil, nil
	case starlark.Bool:
		return bool(value), nil
	case starlark.String:
		return string(value), nil
	case starlark.Int:
		return json.Number(value.String()), nil
	case starlark.Float:
		if !isFinite(float64(value)) {
			return nil, failure("starlark_result_non_finite", "result contains NaN or infinity", nil)
		}
		return float64(value), nil
	case *starlark.List:
		if _, found := active[value]; found {
			return nil, failure("starlark_result_type", "result contains a cycle", nil)
		}
		active[value] = struct{}{}
		defer delete(active, value)
		return sequenceJSONValue(value.Len(), value.Index, active, depth)
	case starlark.Tuple:
		return sequenceJSONValue(value.Len(), value.Index, active, depth)
	case *starlark.Dict:
		if _, found := active[value]; found {
			return nil, failure("starlark_result_type", "result contains a cycle", nil)
		}
		active[value] = struct{}{}
		defer delete(active, value)
		result := make(map[string]any, value.Len())
		for _, item := range value.Items() {
			key, ok := item[0].(starlark.String)
			if !ok {
				return nil, failure("starlark_result_type", "result dictionaries require string keys", nil)
			}
			converted, err := resultJSONValue(item[1], active, depth+1)
			if err != nil {
				return nil, err
			}
			result[string(key)] = converted
		}
		return result, nil
	default:
		return nil, failure("starlark_result_type", "result is not JSON-compatible", nil)
	}
}

func sequenceJSONValue(length int, index func(int) starlark.Value, active map[starlark.Value]struct{}, depth int) (any, error) {
	result := make([]any, length)
	for item := range length {
		converted, err := resultJSONValue(index(item), active, depth+1)
		if err != nil {
			return nil, err
		}
		result[item] = converted
	}
	return result, nil
}

func classifyEvaluationError(err error, stdout string, steps uint64) error {
	message := err.Error()
	var syntaxErr syntax.Error
	if errors.As(err, &syntaxErr) {
		return failure("starlark_parse_error", "program could not be parsed", err)
	}
	if strings.Contains(message, "too many steps") {
		return failure("starlark_step_limit", "evaluation exceeded max_steps", err)
	}
	if strings.Contains(message, "undefined:") {
		return failure("starlark_name_error", "program referenced an unavailable name", err)
	}
	if strings.Contains(message, "syntax error") || strings.Contains(message, "got") {
		return failure("starlark_parse_error", "program could not be parsed", err)
	}
	if stdout != "" || steps > 0 {
		return failure("starlark_runtime_error", "program evaluation failed", err)
	}
	return failure("starlark_parse_error", "program could not be parsed", err)
}

func isFiniteBuiltin(_ *starlark.Thread, _ *starlark.Builtin, args starlark.Tuple, kwargs []starlark.Tuple) (starlark.Value, error) {
	var value starlark.Value
	if err := starlark.UnpackArgs("_is_finite", args, kwargs, "value", &value); err != nil {
		return nil, err
	}
	float, ok := value.(starlark.Float)
	if !ok {
		return nil, fmt.Errorf("_is_finite requires float")
	}
	return starlark.Bool(isFinite(float64(float))), nil
}

func failure(code, message string, err error) *Error {
	return &Error{Code: code, Message: message, Err: err}
}

func isFinite(value float64) bool { return !math.IsInf(value, 0) && !math.IsNaN(value) }

type limitedBuffer struct {
	bytes.Buffer
	limit    int
	exceeded bool
}

func (b *limitedBuffer) WriteString(value string) (int, error) {
	if b.Len()+len(value) > b.limit {
		remaining := max(0, b.limit-b.Len())
		_, _ = b.Buffer.WriteString(value[:remaining])
		b.exceeded = true
		return len(value), nil
	}
	return b.Buffer.WriteString(value)
}
