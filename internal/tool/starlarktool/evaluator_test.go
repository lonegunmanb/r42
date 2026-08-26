package starlarktool

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestEvaluateReturnsCanonicalJSONAndCapturedOutput(t *testing.T) {
	t.Parallel()
	result, err := Evaluate(context.Background(), DefaultConfig(), `
print("calculating")
result = {
  "total": data["first"] + data["second"],
  "values": [1, 2.5, None],
}
`, `{"first": 2, "second": 3}`)

	require.NoError(t, err)
	assert.JSONEq(t, `{"total":5,"values":[1,2.5,null]}`, result.ResultJSON)
	assert.Equal(t, "calculating\n", result.Stdout)
	assert.Positive(t, result.Steps)
}

func TestEvaluateProvidesStatsAndMatrixModules(t *testing.T) {
	t.Parallel()
	result, err := Evaluate(context.Background(), DefaultConfig(), `
values = [1, 2, 3]
result = {
  "mean": stats.mean(values),
  "variance": stats.variance(values),
  "product": matrix.matmul([[1, 2]], [[3], [4]]),
  "shape": matrix.shape([[1, 2], [3, 4]]),
}
`, "null")

	require.NoError(t, err)
	assert.JSONEq(t, `{"mean":2.0,"variance":1.0,"product":[[11]],"shape":[2,2]}`, result.ResultJSON)
}

func TestEvaluateSupportsDocumentedNumericalFunctions(t *testing.T) {
	t.Parallel()
	result, err := Evaluate(context.Background(), DefaultConfig(), `
values = [1, 2, 3]
result = [
  stats.mean(values), stats.median([1, 3, 2, 4]),
  stats.variance(values), stats.pvariance(values),
  stats.stdev(values), stats.pstdev(values),
  stats.covariance(values, [2, 4, 6]),
  matrix.transpose([[1, 2], [3, 4]]),
]
`, "null")

	require.NoError(t, err)
	assert.JSONEq(t, `[
  2, 2.5, 1, 0.6666666666666666, 1, 0.816496580927726, 2,
  [[1,3],[2,4]]
]`, result.ResultJSON)
}

func TestEvaluateRejectsMutatedDataAndOutputLimits(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name   string
		config Config
		code   string
		want   string
	}{
		{name: "frozen data", code: "data.append(1)\nresult = data", want: "starlark_runtime_error"},
		{name: "stdout", config: Config{MaxStdoutBytes: 3}, code: "print(\"four\")\nresult = 1", want: "starlark_output_limit"},
		{name: "result", config: Config{MaxResultBytes: 3}, code: "result = \"four\"", want: "starlark_output_limit"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			_, err := Evaluate(context.Background(), tt.config, tt.code, "[]")
			require.Error(t, err)
			var evaluationErr *Error
			require.ErrorAs(t, err, &evaluationErr)
			assert.Equal(t, tt.want, evaluationErr.Code)
		})
	}
}

func TestEvaluateRejectsInvalidNumericalInputs(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name string
		code string
	}{
		{name: "empty mean", code: "result = stats.mean([])"},
		{name: "boolean statistic", code: "result = stats.mean([True])"},
		{name: "sample variance", code: "result = stats.variance([1])"},
		{name: "unequal covariance", code: "result = stats.covariance([1, 2], [1])"},
		{name: "ragged matrix", code: "result = matrix.shape([[1], [2, 3]])"},
		{name: "empty matrix", code: "result = matrix.transpose([])"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			_, err := Evaluate(context.Background(), DefaultConfig(), tt.code, "null")
			require.Error(t, err)
			var evaluationErr *Error
			require.ErrorAs(t, err, &evaluationErr)
			assert.Equal(t, "starlark_runtime_error", evaluationErr.Code)
		})
	}
}

func TestEvaluateRejectsNonJSONResultValues(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name string
		code string
		want string
	}{
		{name: "function", code: "result = lambda: 1", want: "starlark_result_type"},
		{name: "non string key", code: "result = {1: \"no\"}", want: "starlark_result_type"},
		{name: "non finite", code: "result = math.sqrt(-1)", want: "starlark_result_non_finite"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			_, err := Evaluate(context.Background(), DefaultConfig(), tt.code, "null")
			require.Error(t, err)
			var evaluationErr *Error
			require.ErrorAs(t, err, &evaluationErr)
			assert.Equal(t, tt.want, evaluationErr.Code)
		})
	}
}

func TestEvaluateClassifiesParseErrorsAndRejectsNonFiniteNumericalInputs(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name string
		code string
		want string
	}{
		{name: "parse", code: "result = (", want: "starlark_parse_error"},
		{name: "stats non finite", code: "result = stats.mean([math.sqrt(-1)])", want: "starlark_runtime_error"},
		{name: "matrix non finite", code: "result = matrix.shape([[math.sqrt(-1)]])", want: "starlark_runtime_error"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			_, err := Evaluate(context.Background(), DefaultConfig(), tt.code, "null")
			require.Error(t, err)
			var evaluationErr *Error
			require.ErrorAs(t, err, &evaluationErr)
			assert.Equal(t, tt.want, evaluationErr.Code)
		})
	}
}

func TestEvaluateClassifiesRepairableFailures(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name   string
		code   string
		data   string
		config Config
		want   string
	}{
		{name: "bad data", code: "result = 1", data: "nope", want: "starlark_data_json"},
		{name: "missing result", code: "value = 1", data: "null", want: "starlark_result_missing"},
		{name: "bad name", code: "result = absent", data: "null", want: "starlark_name_error"},
		{name: "bad matrix", code: "result = matrix.matmul([[1, 2]], [[1, 2]])", data: "null", want: "starlark_runtime_error"},
		{name: "step limit", code: "def calculate():\n  total = 0\n  for item in range(100000):\n    total += item\n  return total\nresult = calculate()", data: "null", config: Config{MaxSteps: 100}, want: "starlark_step_limit"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			_, err := Evaluate(context.Background(), tt.config, tt.code, tt.data)
			require.Error(t, err)
			var evaluationErr *Error
			require.ErrorAs(t, err, &evaluationErr)
			assert.Equal(t, tt.want, evaluationErr.Code)
		})
	}
}

func TestEvaluateClassifiesContextDeadlineAsTimeout(t *testing.T) {
	t.Parallel()
	ctx, cancel := context.WithDeadline(t.Context(), time.Now().Add(-time.Second))
	cancel()

	_, err := Evaluate(ctx, DefaultConfig(), "result = 1", "null")

	require.Error(t, err)
	var evaluationErr *Error
	require.ErrorAs(t, err, &evaluationErr)
	assert.Equal(t, "starlark_timeout", evaluationErr.Code)
}

func TestEvaluateStopsAnActiveProgramWhenTimeoutExpires(t *testing.T) {
	t.Parallel()
	ctx, cancel := context.WithTimeout(t.Context(), 10*time.Millisecond)
	defer cancel()

	_, err := Evaluate(ctx, Config{MaxSteps: 10_000_000}, "def calculate():\n  total = 0\n  for item in range(100000000):\n    total += item\n  return total\nresult = calculate()", "null")

	require.Error(t, err)
	var evaluationErr *Error
	require.ErrorAs(t, err, &evaluationErr)
	assert.Equal(t, "starlark_timeout", evaluationErr.Code)
}
