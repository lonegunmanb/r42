package progress_test

import (
	"strings"
	"testing"
	"unicode/utf8"

	"github.com/lonegunmanb/r42/internal/debuglog"
	"github.com/lonegunmanb/r42/internal/plan"
	"github.com/lonegunmanb/r42/internal/progress"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func buildDynamicPlan(t *testing.T, runDirectory string) *plan.Plan {
	t.Helper()
	planned, err := plan.NewForRun("root", runDirectory, []plan.NodeSpec{
		{Address: "research.dynamic.followups", Kind: "research"},
		{
			Address: "research.static.summary", Kind: "research",
			Dependencies: []string{"research.dynamic.followups"},
		},
	}, nil, nil, nil)
	require.NoError(t, err)
	return planned
}

func materializeEvent(address string, paths ...string) debuglog.Event {
	return debuglog.Event{
		Kind: debuglog.EventLifecycle, Action: "dynamic.tasks.materialized",
		Status: debuglog.StatusCompleted, BlockAddress: address, BlockType: "research",
		Paths: paths, Count: len(paths),
	}
}

func TestProjectorMaterializesDynamicTasksWithCanonicalAddresses(t *testing.T) {
	t.Parallel()

	planned := buildDynamicPlan(t, testRunDirectory(t, "run-42"))
	projector := progress.NewProjector(planned)

	projector.Observe(materializeEvent("research.dynamic.followups",
		"research.dynamic.followups.tasks[0]",
		"research.dynamic.followups.tasks[1]",
	))

	snapshot := projector.Snapshot()
	addresses := make(map[string]progress.NodeProjection)
	for _, node := range snapshot.Nodes {
		addresses[node.BlockAddress] = node
	}

	// Dynamic members appear with canonical, unambiguous addresses.
	require.Contains(t, addresses, "research.dynamic.followups.tasks[0]")
	require.Contains(t, addresses, "research.dynamic.followups.tasks[1]")
	require.Len(t, addresses, 4) // parent + 2 dynamic tasks + summary

	// Dynamic children point back at the parent with a research kind.
	task := addresses["research.dynamic.followups.tasks[0]"]
	assert.Equal(t, "research", task.BlockKind)
	assert.Equal(t, "research.dynamic.followups", task.ParentAddress)
	assert.Equal(t, progress.StatusWaiting, task.Status)
}

func TestProjectorDoesNotDuplicateDynamicMaterialization(t *testing.T) {
	t.Parallel()

	planned := buildDynamicPlan(t, testRunDirectory(t, "run-42"))
	projector := progress.NewProjector(planned)
	address := "research.dynamic.followups"

	// The same task set is materialized twice; the node map must not grow.
	event := materializeEvent(address,
		address+".tasks[0]", address+".tasks[1]",
	)
	projector.Observe(event)
	projector.Observe(event)

	assert.Len(t, projector.Snapshot().Nodes, 4)
}

func TestProjectorIgnoresNonCanonicalDynamicTaskAddresses(t *testing.T) {
	t.Parallel()

	planned := buildDynamicPlan(t, testRunDirectory(t, "run-42"))
	projector := progress.NewProjector(planned)
	parent := "research.dynamic.followups"
	projector.Observe(materializeEvent(parent,
		parent+".tasks[0]",
		"research.static.summary",
		parent+".other[1]",
		parent+".tasks[no]",
	))

	record := projector.Materialized(parent)
	require.NotNil(t, record)
	require.Len(t, record.Nodes, 1)
	assert.Equal(t, parent+".tasks[0]", record.Nodes[0].BlockAddress)
	assert.Len(t, projector.Snapshot().Nodes, 3)
}

func TestProjectorTracksDynamicTaskPhaseStatusAndActivity(t *testing.T) {
	t.Parallel()

	planned := buildDynamicPlan(t, testRunDirectory(t, "run-42"))
	projector := progress.NewProjector(planned)
	taskAddress := "research.dynamic.followups.tasks[0]"
	projector.Observe(materializeEvent("research.dynamic.followups", taskAddress))

	projector.Observe(debuglog.Event{
		Kind: debuglog.EventLifecycle, Action: "block.apply", Status: debuglog.StatusStarted,
		BlockAddress: taskAddress, BlockType: "research",
	})
	projector.Observe(debuglog.Event{
		Kind: debuglog.EventMessage, Action: "assistant.reasoning_delta",
		BlockAddress: taskAddress, Session: debuglog.SessionResearch,
		Content: "gathering evidence",
	})

	task := projector.MustNode(taskAddress)
	assert.Equal(t, progress.StatusRunning, task.Status)
	assert.Equal(t, progress.PhaseResearch, task.Phase)
	assert.Equal(t, progress.ActivityThinking, task.Activity)

	projector.Observe(debuglog.Event{
		Kind: debuglog.EventLifecycle, Action: "block.apply", Status: debuglog.StatusCompleted,
		BlockAddress: taskAddress, BlockType: "research",
	})
	task = projector.MustNode(taskAddress)
	assert.Equal(t, progress.StatusSucceeded, task.Status)
	assert.Equal(t, progress.ActivityIdle, task.Activity)
}

func TestProjectorNodeUpsertIsCompleteReplacement(t *testing.T) {
	t.Parallel()

	planned := buildProjectorPlan(t, testRunDirectory(t, "run-42"))
	projector := progress.NewProjector(planned)
	address := "research.static.frame"

	projector.Observe(debuglog.Event{
		Kind: debuglog.EventLifecycle, Action: "block.apply", Status: debuglog.StatusStarted,
		BlockAddress: address, BlockType: "research",
	})
	projector.Observe(debuglog.Event{
		Kind: debuglog.EventTool, Action: "tool.execution_start",
		BlockAddress: address, Session: debuglog.SessionResearch,
		ToolName: "web_fetch",
	})
	projector.Observe(debuglog.Event{
		Kind: debuglog.EventLifecycle, Action: "block.apply", Status: debuglog.StatusCompleted,
		BlockAddress: address, BlockType: "research",
	})

	// The projection is the complete, latest state — not a delta accumulated
	// from the event history. Status reflects completion and activity is idle,
	// while the tool name retains the last tool used (matching the TUI).
	node := projector.MustNode(address)
	assert.Equal(t, progress.StatusSucceeded, node.Status)
	assert.Equal(t, progress.ActivityIdle, node.Activity)
	assert.Equal(t, "web_fetch", node.ToolName)
}

func TestProjectorProvidesPerBlockTimelineSummary(t *testing.T) {
	t.Parallel()

	planned := buildProjectorPlan(t, testRunDirectory(t, "run-42"))
	projector := progress.NewProjector(planned)
	address := "research.static.frame"

	projector.Observe(debuglog.Event{
		Kind: debuglog.EventMessage, Action: "assistant.message",
		BlockAddress: address, Session: debuglog.SessionResearch,
		Content: "final answer \x1b[32mdone\x1b[0m",
	})

	timeline := projector.Timeline(address)
	require.NotNil(t, timeline)
	assert.Equal(t, address, timeline.BlockAddress)
	assert.Equal(t, progress.ActivityReplying, timeline.Activity)
	// The summary is the sanitized, bounded assistant-derived text.
	assert.Equal(t, "final answer done", timeline.Summary)
}

func TestProjectorProvidesSafeTimelineSummaryForToolOnlyActivity(t *testing.T) {
	t.Parallel()

	planned := buildProjectorPlan(t, testRunDirectory(t, "run-42"))
	projector := progress.NewProjector(planned)
	address := "research.static.frame"
	projector.Observe(debuglog.Event{
		Kind: debuglog.EventTool, Action: "tool.execution_start",
		BlockAddress: address, Session: debuglog.SessionResearch,
		ToolName: "web_fetch", Arguments: []byte(`{"url":"https://secret.example"}`),
		Result: []byte(`{"page":"secret result"}`),
	})

	timeline := projector.Timeline(address)
	require.NotNil(t, timeline)
	assert.Equal(t, progress.ActivityTool, timeline.Activity)
	assert.Equal(t, "Running web_fetch", timeline.Summary)
	assert.NotContains(t, timeline.Summary, "secret")
}

func TestProjectorBoundsToolTimelineSummary(t *testing.T) {
	t.Parallel()

	planned := buildProjectorPlan(t, testRunDirectory(t, "run-42"))
	projector := progress.NewProjector(planned)
	projector.Observe(debuglog.Event{
		Kind: debuglog.EventTool, Action: "tool.execution_start",
		BlockAddress: "research.static.frame", Session: debuglog.SessionResearch,
		ToolName: strings.Repeat("tool", 2000),
	})

	timeline := projector.Timeline("research.static.frame")
	require.NotNil(t, timeline)
	assert.LessOrEqual(t, len(timeline.Summary), 4096)
	assert.True(t, utf8.ValidString(timeline.Summary))
}

func TestProjectorTimelineForUnknownAddressIsNil(t *testing.T) {
	t.Parallel()

	planned := buildProjectorPlan(t, testRunDirectory(t, "run-42"))
	projector := progress.NewProjector(planned)

	assert.Nil(t, projector.Timeline("no.such.block"))
}

func TestProjectorTimelineNilBeforeAnyAssistantText(t *testing.T) {
	t.Parallel()

	planned := buildProjectorPlan(t, testRunDirectory(t, "run-42"))
	projector := progress.NewProjector(planned)
	address := "research.static.frame"

	// Lifecycle-only events produce no assistant-derived summary, so the
	// timeline record is nil and would not be emitted on the wire (the wire
	// TimelineRecord requires a non-empty summary).
	projector.Observe(debuglog.Event{
		Kind: debuglog.EventLifecycle, Action: "block.apply", Status: debuglog.StatusStarted,
		BlockAddress: address, BlockType: "research",
	})

	assert.Nil(t, projector.Timeline(address))
}

func TestProjectorDoesNotRetainTimelineHistory(t *testing.T) {
	t.Parallel()

	planned := buildProjectorPlan(t, testRunDirectory(t, "run-42"))
	projector := progress.NewProjector(planned)
	address := "research.static.frame"

	// A large volume of timeline-bearing events must not grow projector
	// memory: only the latest per-block summary is retained.
	for range 1000 {
		projector.Observe(debuglog.Event{
			Kind: debuglog.EventMessage, Action: "assistant.message_delta",
			BlockAddress: address, Session: debuglog.SessionResearch,
			Content: "delta",
		})
	}

	assert.Len(t, projector.Snapshot().Nodes, 4)
	timeline := projector.Timeline(address)
	require.NotNil(t, timeline)
	assert.Equal(t, "delta", timeline.Summary)
}

func TestProjectorSnapshotRecordCoversDynamicAndStaticNodes(t *testing.T) {
	t.Parallel()

	planned := buildDynamicPlan(t, testRunDirectory(t, "run-42"))
	projector := progress.NewProjector(planned)
	projector.Observe(materializeEvent("research.dynamic.followups",
		"research.dynamic.followups.tasks[0]",
	))

	record := projector.SnapshotRecord()
	require.NotNil(t, record)
	addresses := make(map[string]struct{})
	for _, node := range record.Nodes {
		addresses[node.BlockAddress] = struct{}{}
	}
	assert.Contains(t, addresses, "research.dynamic.followups")
	assert.Contains(t, addresses, "research.dynamic.followups.tasks[0]")
	assert.Contains(t, addresses, "research.static.summary")
}

func TestProjectorMaterializedRecordListsNewDynamicNodes(t *testing.T) {
	t.Parallel()

	planned := buildDynamicPlan(t, testRunDirectory(t, "run-42"))
	projector := progress.NewProjector(planned)
	parent := "research.dynamic.followups"
	projector.Observe(materializeEvent(parent, parent+".tasks[0]", parent+".tasks[1]"))

	record := projector.Materialized(parent)
	require.NotNil(t, record)
	assert.Equal(t, parent, record.ParentAddress)
	addresses := make([]string, 0, len(record.Nodes))
	for _, node := range record.Nodes {
		addresses = append(addresses, node.BlockAddress)
	}
	assert.ElementsMatch(t, []string{parent + ".tasks[0]", parent + ".tasks[1]"}, addresses)
}

func TestProjectorMaterializedForStaticBlockIsEmpty(t *testing.T) {
	t.Parallel()

	planned := buildProjectorPlan(t, testRunDirectory(t, "run-42"))
	projector := progress.NewProjector(planned)

	record := projector.Materialized("research.static.frame")
	require.NotNil(t, record)
	assert.Empty(t, record.Nodes)
}

func TestProjectorMaterializedEmptyTaskSetIsValidRecord(t *testing.T) {
	t.Parallel()

	planned := buildDynamicPlan(t, testRunDirectory(t, "run-42"))
	projector := progress.NewProjector(planned)
	parent := "research.dynamic.followups"
	projector.Observe(materializeEvent(parent))

	record := projector.Materialized(parent)
	require.NotNil(t, record)
	require.NotNil(t, record.Nodes)
	assert.Empty(t, record.Nodes)
	assert.NoError(t, record.Validate())
}
