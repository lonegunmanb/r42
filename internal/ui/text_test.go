package ui_test

import (
	"bytes"
	"testing"

	"github.com/lonegunmanb/r42/internal/debuglog"
	"github.com/lonegunmanb/r42/internal/plan"
	"github.com/lonegunmanb/r42/internal/ui"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestTextRendererShowsWorkflowPhaseTransition(t *testing.T) {
	t.Parallel()

	planned, err := plan.NewWithContextAndLocals("root", []plan.NodeSpec{{Address: "research.static.collect", Kind: "research"}}, nil, nil, nil)
	require.NoError(t, err)
	var output bytes.Buffer
	renderer := ui.NewTextRenderer(&output, ui.NewProjector(planned))

	renderer.Observe(debuglog.Event{
		Kind: debuglog.EventLifecycle, Action: "workflow.phase", Status: debuglog.StatusStarted,
		BlockAddress: "research.static.collect", Session: debuglog.SessionCollection, Count: 2, Content: "needs_more",
	})

	assert.Contains(t, output.String(), "[collection] PHASE round=2 decision=needs_more")
}

func TestTextRendererShowsOnlyCreatedSinglePhaseSessions(t *testing.T) {
	t.Parallel()

	planned, err := plan.NewWithContextAndLocals("root", []plan.NodeSpec{
		{Address: "research.static.builder", Kind: "research"},
		{Address: "research.static.juror", Kind: "research"},
	}, nil, nil, nil)
	require.NoError(t, err)
	var output bytes.Buffer
	renderer := ui.NewTextRenderer(&output, ui.NewProjector(planned))

	for _, event := range []debuglog.Event{
		{Kind: debuglog.EventLifecycle, Action: "session.open", Status: debuglog.StatusStarted, BlockAddress: "research.static.builder", Session: debuglog.SessionCollection},
		{Kind: debuglog.EventMessage, Action: "assistant.message", BlockAddress: "research.static.builder", Session: debuglog.SessionCollection, Content: "DCF complete"},
		{Kind: debuglog.EventLifecycle, Action: "session.open", Status: debuglog.StatusStarted, BlockAddress: "research.static.juror", Session: debuglog.SessionResearch},
		{Kind: debuglog.EventMessage, Action: "assistant.message", BlockAddress: "research.static.juror", Session: debuglog.SessionResearch, Content: "review complete"},
	} {
		renderer.Observe(event)
	}

	text := output.String()
	assert.Contains(t, text, "[collection] REPLYING DCF complete")
	assert.Contains(t, text, "[research] REPLYING review complete")
	assert.NotContains(t, text, "[collection_qc]")
	assert.NotContains(t, text, "[final_qc]")
	assert.NotContains(t, text, "[revision]")
}

func TestTextRendererShowsRunDAGAndMeaningfulTransitions(t *testing.T) {
	t.Parallel()
	runDirectory := testRunDirectory(t, "run-42")

	planned, err := plan.NewForRun("root", runDirectory, []plan.NodeSpec{
		{Address: "research.static.collect", Kind: "research"},
		{Address: "research.static.summary", Kind: "research", Dependencies: []string{"research.static.collect"}},
	}, nil, nil, nil)
	require.NoError(t, err)
	projector := ui.NewProjector(planned)
	var output bytes.Buffer
	renderer := ui.NewTextRenderer(&output, projector)

	require.NoError(t, renderer.Start())
	renderer.Observe(debuglog.Event{
		Kind: debuglog.EventLifecycle, Action: "block.apply", Status: debuglog.StatusStarted,
		BlockAddress: "research.static.collect", BlockType: "research",
	})
	renderer.Observe(debuglog.Event{
		Kind: debuglog.EventMessage, Action: "assistant.reasoning_delta",
		BlockAddress: "research.static.collect", Session: debuglog.SessionResearch,
		Content: "evaluating evidence",
	})
	renderer.Observe(debuglog.Event{
		Kind: debuglog.EventTool, Action: "tool.execution_start",
		BlockAddress: "research.static.collect", Session: debuglog.SessionResearch,
		ToolName: "pplx_fetch",
	})
	renderer.Observe(debuglog.Event{
		Kind: debuglog.EventLifecycle, Action: "block.apply", Status: debuglog.StatusCompleted,
		BlockAddress: "research.static.collect", BlockType: "research",
	})

	text := output.String()
	assert.Contains(t, text, "Run: "+runDirectory)
	assert.Contains(t, text, "Research tasks: 2")
	assert.Contains(t, text, "research.static.collect -> research.static.summary")
	assert.Contains(t, text, "[1/2] START research.static.collect")
	assert.Contains(t, text, "[research] THINKING evaluating evidence")
	assert.Contains(t, text, "[research] TOOL pplx_fetch")
	assert.Contains(t, text, "[1/2] DONE research.static.collect")
}

func TestTextRendererShowsMaterializedDynamicTasksWithoutZeroOrdinal(t *testing.T) {
	t.Parallel()
	runDirectory := testRunDirectory(t, "run-42")

	planned, err := plan.NewForRun("root", runDirectory, []plan.NodeSpec{
		{Address: "research.dynamic.followups", Kind: "research"},
	}, nil, nil, nil)
	require.NoError(t, err)
	projector := ui.NewProjector(planned)
	var output bytes.Buffer
	renderer := ui.NewTextRenderer(&output, projector)
	require.NoError(t, renderer.Start())

	renderer.Observe(debuglog.Event{
		Kind: debuglog.EventLifecycle, Action: "dynamic.tasks.materialized",
		Status: debuglog.StatusCompleted, BlockAddress: "research.dynamic.followups",
		BlockType: "research", Count: 1,
		Paths: []string{"research.dynamic.followups.tasks[0]"},
	})
	renderer.Observe(debuglog.Event{
		Kind: debuglog.EventLifecycle, Action: "block.apply", Status: debuglog.StatusStarted,
		BlockAddress: "research.dynamic.followups.tasks[0]", BlockType: "research",
	})
	renderer.Observe(debuglog.Event{
		Kind: debuglog.EventLifecycle, Action: "block.apply", Status: debuglog.StatusCompleted,
		BlockAddress: "research.dynamic.followups", BlockType: "research",
	})

	text := output.String()
	assert.Contains(t, text, "[dynamic] MATERIALIZED research.dynamic.followups tasks=1")
	assert.Contains(t, text, "[1/1] START research.dynamic.followups.tasks[0]")
	assert.NotContains(t, text, "[0/1]")
}

func TestTextRendererRemovesTerminalControlSequencesFromModelContent(t *testing.T) {
	t.Parallel()
	runDirectory := testRunDirectory(t, "run-42")

	planned, err := plan.NewForRun("root", runDirectory, []plan.NodeSpec{
		{Address: "research.static.collect", Kind: "research"},
	}, nil, nil, nil)
	require.NoError(t, err)
	projector := ui.NewProjector(planned)
	var output bytes.Buffer
	renderer := ui.NewTextRenderer(&output, projector)
	require.NoError(t, renderer.Start())

	renderer.Observe(debuglog.Event{
		Kind: debuglog.EventMessage, Action: "assistant.reasoning_delta",
		BlockAddress: "research.static.collect", Session: debuglog.SessionResearch,
		Content: "safe\x1b[2Jcontent\rforged\a",
	})

	assert.NotContains(t, output.String(), "\x1b")
	assert.NotContains(t, output.String(), "\r")
	assert.NotContains(t, output.String(), "\a")
	assert.Contains(t, output.String(), "safecontentforged")
	assert.Equal(t, "safecontentforged", projector.Snapshot().MustNode("research.static.collect").Content)
}

func TestTextRendererShowsNestedModuleLifecycleTransitions(t *testing.T) {
	t.Parallel()
	runDirectory := testRunDirectory(t, "run-module")

	grandchild, err := plan.NewWithContextAndLocals("grandchild", []plan.NodeSpec{
		{Address: "research.static.fetch", Kind: "research"},
	}, nil, nil, nil)
	require.NoError(t, err)
	child, err := plan.NewWithContextAndLocals("child", []plan.NodeSpec{
		{Address: "module.fetchers", Kind: "module", Module: &plan.ModuleSpec{Plan: grandchild}},
	}, nil, nil, nil)
	require.NoError(t, err)
	planned, err := plan.NewForRun("root", runDirectory, []plan.NodeSpec{
		{Address: "module.sources", Kind: "module", Module: &plan.ModuleSpec{Plan: child}},
	}, nil, nil, nil)
	require.NoError(t, err)
	projector := ui.NewProjector(planned)
	var output bytes.Buffer
	renderer := ui.NewTextRenderer(&output, projector)
	require.NoError(t, renderer.Start())

	renderer.Observe(debuglog.Event{
		Kind: debuglog.EventLifecycle, Action: "block.apply", Status: debuglog.StatusStarted,
		BlockAddress: "module.sources", BlockType: "module",
	})
	renderer.Observe(debuglog.Event{
		Kind: debuglog.EventLifecycle, Action: "block.apply", Status: debuglog.StatusCompleted,
		BlockAddress: "module.sources", BlockType: "module",
	})
	renderer.Observe(debuglog.Event{
		Kind: debuglog.EventLifecycle, Action: "block.factory", Status: debuglog.StatusFailed,
		BlockAddress: "module.sources.module.fetchers", BlockType: "module", Error: "load failed",
	})

	text := output.String()
	assert.Contains(t, text, "[module] START module.sources")
	assert.Contains(t, text, "[module] DONE module.sources")
	assert.Contains(t, text, "[module] FAILED module.sources.module.fetchers load failed")
}
