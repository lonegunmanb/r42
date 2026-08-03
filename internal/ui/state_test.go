package ui_test

import (
	"testing"

	"github.com/lonegunmanb/r42/internal/debuglog"
	"github.com/lonegunmanb/r42/internal/plan"
	"github.com/lonegunmanb/r42/internal/ui"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestProjectorBuildsExpandedResearchDAGAndTracksActivity(t *testing.T) {
	t.Parallel()

	child, err := plan.NewWithContextAndLocals("child", []plan.NodeSpec{
		{Address: "research.static.collect", Kind: "research"},
	}, nil, nil, nil)
	require.NoError(t, err)
	planned, err := plan.NewForRun("root", "D:/run-42", []plan.NodeSpec{
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
	projector := ui.NewProjector(planned)

	projector.Observe(debuglog.Event{
		Kind: debuglog.EventLifecycle, Action: "block.apply", Status: debuglog.StatusStarted,
		BlockAddress: "module.sources.research.static.collect", BlockType: "research",
	})
	projector.Observe(debuglog.Event{
		Kind: debuglog.EventMessage, Action: "assistant.reasoning_delta",
		BlockAddress: "module.sources.research.static.collect", Session: debuglog.SessionResearch,
		Role: debuglog.RoleAssistant, Content: "checking primary sources",
	})

	snapshot := projector.Snapshot()
	assert.Equal(t, "D:/run-42", snapshot.RunDirectory)
	assert.Equal(t, 3, snapshot.Research.Total)
	assert.Equal(t, 1, snapshot.Research.Running)
	node, ok := snapshot.Node("module.sources.research.static.collect")
	require.True(t, ok)
	assert.Equal(t, ui.StatusRunning, node.Status)
	assert.Equal(t, ui.ActivityThinking, node.Activity)
	assert.Equal(t, "checking primary sources", node.Content)
	assert.Equal(t, []string{"research.static.frame"}, snapshot.MustNode("module.sources").Dependencies)
	assert.Contains(t, ui.RenderDAG(snapshot), "research.static.summary")
}

func TestProjectorReplacesDynamicResearchCountWithMaterializedTasks(t *testing.T) {
	t.Parallel()

	planned, err := plan.NewWithContextAndLocals("root", []plan.NodeSpec{
		{Address: "research.dynamic.followups", Kind: "research"},
		{
			Address: "research.static.summary", Kind: "research",
			Dependencies: []string{"research.dynamic.followups"},
		},
	}, nil, nil, nil)
	require.NoError(t, err)
	projector := ui.NewProjector(planned)
	assert.Equal(t, 2, projector.Snapshot().Research.Total)

	projector.Observe(debuglog.Event{
		Kind: debuglog.EventLifecycle, Action: "dynamic.tasks.materialized",
		Status: debuglog.StatusCompleted, BlockAddress: "research.dynamic.followups",
		BlockType: "research", Count: 2,
		Paths: []string{
			"research.dynamic.followups.tasks[0]",
			"research.dynamic.followups.tasks[1]",
		},
	})
	projector.Observe(debuglog.Event{
		Kind: debuglog.EventLifecycle, Action: "block.apply", Status: debuglog.StatusStarted,
		BlockAddress: "research.dynamic.followups.tasks[0]", BlockType: "research",
	})

	snapshot := projector.Snapshot()
	assert.Equal(t, 3, snapshot.Research.Total)
	assert.Equal(t, 1, snapshot.Research.Running)
	first, ok := snapshot.Node("research.dynamic.followups.tasks[0]")
	require.True(t, ok)
	second, ok := snapshot.Node("research.dynamic.followups.tasks[1]")
	require.True(t, ok)
	assert.Equal(t, "research.dynamic.followups", first.Parent)
	assert.Equal(t, ui.StatusWaiting, second.Status)
	assert.Equal(t, []string{"research.dynamic.followups"}, snapshot.MustNode("research.static.summary").Dependencies)
}

func TestProjectorRemovesEmptyDynamicResearchFromTaskCount(t *testing.T) {
	t.Parallel()

	planned, err := plan.NewWithContextAndLocals("root", []plan.NodeSpec{
		{Address: "research.dynamic.followups", Kind: "research"},
	}, nil, nil, nil)
	require.NoError(t, err)
	projector := ui.NewProjector(planned)

	projector.Observe(debuglog.Event{
		Kind: debuglog.EventLifecycle, Action: "dynamic.tasks.materialized",
		Status: debuglog.StatusCompleted, BlockAddress: "research.dynamic.followups",
		BlockType: "research", Count: 0,
	})

	assert.Equal(t, 0, projector.Snapshot().Research.Total)
}

func TestProjectorDeduplicatesUsageByAPICallIDAcrossSessions(t *testing.T) {
	t.Parallel()

	planned, err := plan.NewWithContextAndLocals("root", []plan.NodeSpec{
		{Address: "research.static.market", Kind: "research"},
	}, nil, nil, nil)
	require.NoError(t, err)
	projector := ui.NewProjector(planned)
	usage := debuglog.Usage{
		APICallID: "call-42", InputTokens: 100, OutputTokens: 30,
		ReasoningTokens: 10, CacheReadTokens: 20,
	}

	projector.Observe(debuglog.Event{
		Kind: debuglog.EventMessage, Action: "assistant.usage",
		BlockAddress: "research.static.market", Session: debuglog.SessionResearch, Usage: &usage,
	})
	projector.Observe(debuglog.Event{
		Kind: debuglog.EventMessage, Action: "assistant.usage",
		BlockAddress: "research.static.market", Session: debuglog.SessionResearch, Usage: &usage,
	})
	projector.Observe(debuglog.Event{
		Kind: debuglog.EventMessage, Action: "assistant.usage",
		BlockAddress: "research.static.market", Session: debuglog.SessionQC,
		Usage: &debuglog.Usage{APICallID: "qc-1", InputTokens: 50, OutputTokens: 20, ReasoningTokens: 5},
	})

	snapshot := projector.Snapshot()
	assert.Equal(t, int64(200), snapshot.Usage.TotalTokens())
	assert.Equal(t, int64(150), snapshot.Usage.InputTokens)
	assert.Equal(t, int64(50), snapshot.Usage.OutputTokens)
	assert.Equal(t, int64(15), snapshot.Usage.ReasoningTokens)
	assert.Equal(t, int64(20), snapshot.Usage.CacheReadTokens)
	assert.Equal(t, int64(200), snapshot.MustNode("research.static.market").Usage.TotalTokens())
}

func TestProjectorCoalescesStreamingMessagesAndToolCallsInTimeline(t *testing.T) {
	t.Parallel()

	planned, err := plan.NewWithContextAndLocals("root", []plan.NodeSpec{
		{Address: "research.static.market", Kind: "research"},
	}, nil, nil, nil)
	require.NoError(t, err)
	projector := ui.NewProjector(planned)
	events := []debuglog.Event{
		{
			Kind: debuglog.EventMessage, Action: "assistant.reasoning_delta", MessageID: "reasoning-1",
			BlockAddress: "research.static.market", Session: debuglog.SessionResearch, Content: "inspect ",
		},
		{
			Kind: debuglog.EventMessage, Action: "assistant.reasoning_delta", MessageID: "reasoning-1",
			BlockAddress: "research.static.market", Session: debuglog.SessionResearch, Content: "sources",
		},
		{
			Kind: debuglog.EventMessage, Action: "assistant.reasoning", MessageID: "reasoning-1",
			BlockAddress: "research.static.market", Session: debuglog.SessionResearch, Content: "inspect sources",
		},
		{
			Kind: debuglog.EventTool, Action: "tool.execution_start", ToolCallID: "tool-1",
			BlockAddress: "research.static.market", Session: debuglog.SessionResearch, ToolName: "pplx_fetch",
		},
		{
			Kind: debuglog.EventTool, Action: "tool.execution_progress", ToolCallID: "tool-1",
			BlockAddress: "research.static.market", Session: debuglog.SessionResearch,
			ToolName: "pplx_fetch", Content: "downloading",
		},
		{
			Kind: debuglog.EventTool, Action: "tool.execution_complete", ToolCallID: "tool-1",
			BlockAddress: "research.static.market", Session: debuglog.SessionResearch,
			ToolName: "pplx_fetch",
		},
	}
	for _, event := range events {
		projector.Observe(event)
	}

	timeline := projector.Snapshot().Timeline
	require.Len(t, timeline, 2)
	assert.Equal(t, "assistant.reasoning", timeline[0].Action)
	assert.Equal(t, "inspect sources", timeline[0].Content)
	assert.Equal(t, "tool.execution_complete", timeline[1].Action)
	assert.Equal(t, "pplx_fetch", timeline[1].ToolName)
	assert.Contains(t, timeline[1].Content, "downloading")
}

func TestProjectorResetsContentWhenAssistantStreamChanges(t *testing.T) {
	t.Parallel()

	planned, err := plan.NewWithContextAndLocals("root", []plan.NodeSpec{
		{Address: "research.static.market", Kind: "research"},
	}, nil, nil, nil)
	require.NoError(t, err)
	projector := ui.NewProjector(planned)
	projector.Observe(debuglog.Event{
		Kind: debuglog.EventMessage, Action: "assistant.reasoning_delta", MessageID: "reasoning-1",
		BlockAddress: "research.static.market", Session: debuglog.SessionResearch, Content: "thinking",
	})
	projector.Observe(debuglog.Event{
		Kind: debuglog.EventMessage, Action: "assistant.message_delta", MessageID: "message-1",
		BlockAddress: "research.static.market", Session: debuglog.SessionResearch, Content: "answer",
	})

	node := projector.Snapshot().MustNode("research.static.market")
	assert.Equal(t, "answer", node.Content)
}

func TestProjectorTracksFactoryFailuresAndRevisionPhase(t *testing.T) {
	t.Parallel()

	planned, err := plan.NewWithContextAndLocals("root", []plan.NodeSpec{
		{Address: "research.static.market", Kind: "research"},
	}, nil, nil, nil)
	require.NoError(t, err)
	projector := ui.NewProjector(planned)
	projector.Observe(debuglog.Event{
		Kind: debuglog.EventLifecycle, Action: "block.factory", Status: debuglog.StatusFailed,
		BlockAddress: "research.static.market", BlockType: "research",
	})
	snapshot := projector.Snapshot()
	assert.Equal(t, ui.StatusFailed, snapshot.MustNode("research.static.market").Status)
	assert.Equal(t, 1, snapshot.Research.Failed)
	projector.Observe(debuglog.Event{
		Kind: debuglog.EventMessage, Action: "assistant.reasoning", Session: debuglog.SessionRevision,
		BlockAddress: "research.static.market", Content: "repairing",
	})
	assert.Equal(t, debuglog.SessionRevision, projector.Snapshot().MustNode("research.static.market").Phase)
}

func TestRenderDAGSanitizesPlanAddresses(t *testing.T) {
	t.Parallel()

	planned, err := plan.NewWithContextAndLocals("root", []plan.NodeSpec{
		{Address: "research.static.dep\x1b]0;fake\a", Kind: "research"},
		{Address: "research.static.bad\x1b[2J", Kind: "research", Dependencies: []string{"research.static.dep\x1b]0;fake\a"}},
	}, nil, nil, nil)
	require.NoError(t, err)
	rendered := ui.RenderDAG(ui.NewProjector(planned).Snapshot())
	assert.NotContains(t, rendered, "\x1b")
	assert.NotContains(t, rendered, "\a")
}
