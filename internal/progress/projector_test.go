package progress_test

import (
	"path/filepath"
	"strings"
	"testing"
	"unicode/utf8"

	"github.com/lonegunmanb/r42/internal/debuglog"
	"github.com/lonegunmanb/r42/internal/plan"
	"github.com/lonegunmanb/r42/internal/progress"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func testRunDirectory(t *testing.T, name string) string {
	t.Helper()
	return filepath.Join(t.TempDir(), name)
}

func buildProjectorPlan(t *testing.T, runDirectory string) *plan.Plan {
	t.Helper()
	child, err := plan.NewWithContextAndLocals("child", []plan.NodeSpec{
		{Address: "research.static.collect", Kind: "research"},
	}, nil, nil, nil)
	require.NoError(t, err)
	planned, err := plan.NewForRun("root", runDirectory, []plan.NodeSpec{
		{Address: "research.static.frame", Kind: "research"},
		{
			Address: "module.sources", Kind: "module",
			Dependencies: []string{"research.static.frame"},
			Module:       &plan.ModuleSpec{Plan: child},
		},
		{
			Address: "research.static.summary", Kind: "research",
			Dependencies: []string{"module.sources"},
		},
	}, nil, nil, nil)
	require.NoError(t, err)
	return planned
}

func TestProjectorBuildsExpandedDAGSnapshotIncludingNestedModules(t *testing.T) {
	t.Parallel()

	planned := buildProjectorPlan(t, testRunDirectory(t, "run-42"))
	projector := progress.NewProjector(planned)

	snapshot := projector.Snapshot()

	// The node map reproduces the TUI's expanded DAG including nested module
	// children, without requiring the complete Plan.
	addresses := make(map[string]progress.NodeProjection)
	for _, node := range snapshot.Nodes {
		addresses[node.BlockAddress] = node
	}
	require.Len(t, addresses, 4)
	require.Contains(t, addresses, "research.static.frame")
	require.Contains(t, addresses, "module.sources")
	require.Contains(t, addresses, "module.sources.research.static.collect")
	require.Contains(t, addresses, "research.static.summary")

	// Canonical addresses and dependencies match the TUI's DAG.
	frame := addresses["research.static.frame"]
	assert.Equal(t, progress.Phase(""), frame.Phase)
	assert.Equal(t, progress.StatusWaiting, frame.Status)
	assert.Equal(t, progress.ActivityIdle, frame.Activity)
	collect := addresses["module.sources.research.static.collect"]
	assert.Equal(t, "module.sources", collect.ParentAddress)
	assert.Equal(t, []string{"research.static.frame"}, addresses["module.sources"].Dependencies)
	assert.Equal(t, []string{"module.sources"}, addresses["research.static.summary"].Dependencies)
}

func TestProjectorStateIsLinearInKnownBlocks(t *testing.T) {
	t.Parallel()

	planned := buildProjectorPlan(t, testRunDirectory(t, "run-42"))
	projector := progress.NewProjector(planned)

	// Feed many unrelated events; the node map must not grow beyond the known
	// block set.
	for range 100 {
		projector.Observe(debuglog.Event{
			Kind: debuglog.EventMessage, Action: "assistant.reasoning_delta",
			BlockAddress: "research.static.frame", Session: debuglog.SessionResearch,
			Content: "reasoning",
		})
	}
	projector.Observe(debuglog.Event{
		Kind: debuglog.EventLifecycle, Action: "block.apply", Status: debuglog.StatusStarted,
		BlockAddress: "unknown.block", BlockType: "research",
	})

	assert.Len(t, projector.Snapshot().Nodes, 4)
}

func TestProjectorTracksStatusAndPhaseChanges(t *testing.T) {
	t.Parallel()

	planned := buildProjectorPlan(t, testRunDirectory(t, "run-42"))
	projector := progress.NewProjector(planned)
	address := "research.static.frame"

	projector.Observe(debuglog.Event{
		Kind: debuglog.EventLifecycle, Action: "block.apply", Status: debuglog.StatusStarted,
		BlockAddress: address, BlockType: "research",
	})
	projector.Observe(debuglog.Event{
		Kind: debuglog.EventMessage, Action: "assistant.reasoning_delta",
		BlockAddress: address, Session: debuglog.SessionResearch,
		Content: "checking evidence",
	})

	node := projector.MustNode(address)
	assert.Equal(t, progress.StatusRunning, node.Status)
	assert.Equal(t, progress.PhaseResearch, node.Phase)
	assert.Equal(t, progress.ActivityThinking, node.Activity)

	projector.Observe(debuglog.Event{
		Kind: debuglog.EventLifecycle, Action: "block.apply", Status: debuglog.StatusCompleted,
		BlockAddress: address, BlockType: "research",
	})
	node = projector.MustNode(address)
	assert.Equal(t, progress.StatusSucceeded, node.Status)
	assert.Equal(t, progress.ActivityIdle, node.Activity)
}

func TestProjectorTracksSafeToolNameWithoutArguments(t *testing.T) {
	t.Parallel()

	planned := buildProjectorPlan(t, testRunDirectory(t, "run-42"))
	projector := progress.NewProjector(planned)
	address := "research.static.frame"

	projector.Observe(debuglog.Event{
		Kind: debuglog.EventTool, Action: "tool.execution_start",
		BlockAddress: address, Session: debuglog.SessionResearch,
		ToolName: "web_fetch", Arguments: []byte(`{"url":"https://secret.example"}`),
		Result: []byte(`{"page":"secret content"}`),
	})

	node := projector.MustNode(address)
	assert.Equal(t, progress.ActivityTool, node.Activity)
	assert.Equal(t, "web_fetch", node.ToolName)
	// The projection must not leak tool arguments or results.
	assert.NotContains(t, node.String(), "https://secret.example")
	assert.NotContains(t, node.String(), "secret content")
}

func TestProjectorNormalizesToolNameForWireProjection(t *testing.T) {
	t.Parallel()

	planned := buildProjectorPlan(t, testRunDirectory(t, "run-42"))
	projector := progress.NewProjector(planned)
	address := "research.static.frame"

	projector.Observe(debuglog.Event{
		Kind: debuglog.EventTool, Action: "tool.execution_start",
		BlockAddress: address, Session: debuglog.SessionResearch,
		ToolName: "web\t_fetch\n",
	})

	node := projector.MustNode(address)
	assert.Equal(t, "web_fetch", node.ToolName)
	assert.NoError(t, (&progress.NodeRecord{Node: node}).Validate())
}

func TestProjectorDeduplicatesAggregateTokenUsage(t *testing.T) {
	t.Parallel()

	planned := buildProjectorPlan(t, testRunDirectory(t, "run-42"))
	projector := progress.NewProjector(planned)
	address := "research.static.frame"

	usage := &debuglog.Usage{
		APICallID: "call-1", InputTokens: 100, OutputTokens: 50, ReasoningTokens: 25,
	}
	for range 3 {
		projector.Observe(debuglog.Event{
			Kind: debuglog.EventMessage, Action: "assistant.usage",
			BlockAddress: address, Session: debuglog.SessionResearch,
			Usage: usage,
		})
	}

	node := projector.MustNode(address)
	require.NotNil(t, node.Usage)
	// The duplicate API call ID is counted only once.
	assert.Equal(t, int64(100), node.Usage.Input)
	assert.Equal(t, int64(50), node.Usage.Output)
	assert.Equal(t, int64(25), node.Usage.Reasoning)
}

func TestProjectorAggregatesDistinctTokenUsage(t *testing.T) {
	t.Parallel()

	planned := buildProjectorPlan(t, testRunDirectory(t, "run-42"))
	projector := progress.NewProjector(planned)
	address := "research.static.frame"

	for _, usage := range []*debuglog.Usage{
		{APICallID: "call-1", InputTokens: 100, OutputTokens: 50},
		{APICallID: "call-2", InputTokens: 25, OutputTokens: 10},
	} {
		projector.Observe(debuglog.Event{
			Kind: debuglog.EventMessage, Action: "assistant.usage",
			BlockAddress: address, Session: debuglog.SessionResearch,
			Usage: usage,
		})
	}

	node := projector.MustNode(address)
	require.NotNil(t, node.Usage)
	assert.Equal(t, int64(125), node.Usage.Input)
	assert.Equal(t, int64(60), node.Usage.Output)
}

func TestProjectorStripsTerminalControlCharacters(t *testing.T) {
	t.Parallel()

	planned := buildProjectorPlan(t, testRunDirectory(t, "run-42"))
	projector := progress.NewProjector(planned)
	address := "research.static.frame"

	projector.Observe(debuglog.Event{
		Kind: debuglog.EventMessage, Action: "assistant.message",
		BlockAddress: address, Session: debuglog.SessionResearch,
		Content: "plain \x1b[31mred\x1b[0m text",
	})

	node := projector.MustNode(address)
	assert.NotContains(t, node.Summary(), "\x1b")
	assert.Contains(t, node.Summary(), "red")
}

func TestProjectorTruncatesSummariesTo4096UTF8Bytes(t *testing.T) {
	t.Parallel()

	planned := buildProjectorPlan(t, testRunDirectory(t, "run-42"))
	projector := progress.NewProjector(planned)
	address := "research.static.frame"

	projector.Observe(debuglog.Event{
		Kind: debuglog.EventMessage, Action: "assistant.message",
		BlockAddress: address, Session: debuglog.SessionResearch,
		Content: strings.Repeat("a", 5000),
	})

	node := projector.MustNode(address)
	summary := node.Summary()
	assert.LessOrEqual(t, len(summary), 4096)
	assert.True(t, strings.HasPrefix(summary, strings.Repeat("a", 4096)))
}

func TestProjectorTruncatesSummaryAtUTF8Boundary(t *testing.T) {
	t.Parallel()

	planned := buildProjectorPlan(t, testRunDirectory(t, "run-42"))
	projector := progress.NewProjector(planned)
	address := "research.static.frame"

	// 2048 multi-byte (3-byte) runes exceed the 4096-byte bound mid-rune.
	content := strings.Repeat("界", 2048)
	projector.Observe(debuglog.Event{
		Kind: debuglog.EventMessage, Action: "assistant.message",
		BlockAddress: address, Session: debuglog.SessionResearch,
		Content: content,
	})

	node := projector.MustNode(address)
	summary := node.Summary()
	assert.LessOrEqual(t, len(summary), 4096)
	// Truncation must not cut a multi-byte rune in half.
	assert.True(t, utf8.ValidString(summary))
	assert.True(t, strings.HasSuffix(summary, "界"))
}

func TestProjectorExcludesSensitiveDebugContent(t *testing.T) {
	t.Parallel()

	planned := buildProjectorPlan(t, testRunDirectory(t, "run-42"))
	projector := progress.NewProjector(planned)
	address := "research.static.frame"

	projector.Observe(debuglog.Event{
		Kind: debuglog.EventTool, Action: "tool.execution_complete",
		BlockAddress: address, Session: debuglog.SessionResearch,
		ToolName: "web_fetch", Arguments: []byte(`{"url":"https://secret.example"}`),
		Result: []byte(`{"page":"secret result"}`),
		Stdout: "secret stdout", Stderr: "secret stderr",
		SDKEvent: []byte(`{"sdk":"payload"}`),
	})

	// Raw tool arguments/results, process streams, and SDK payloads must
	// never reach the projection summary; only the safe tool name is kept.
	node := projector.MustNode(address)
	assert.Equal(t, "web_fetch", node.ToolName)
	summary := node.Summary()
	assert.NotContains(t, summary, "https://secret.example")
	assert.NotContains(t, summary, "secret result")
	assert.NotContains(t, summary, "secret stdout")
	assert.NotContains(t, summary, "secret stderr")
	assert.NotContains(t, summary, `"sdk"`)
	assert.NotContains(t, summary, "payload")
}
