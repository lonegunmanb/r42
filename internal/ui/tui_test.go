package ui_test

import (
	"bytes"
	"context"
	"fmt"
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/x/ansi"
	"github.com/lonegunmanb/r42/internal/debuglog"
	"github.com/lonegunmanb/r42/internal/plan"
	"github.com/lonegunmanb/r42/internal/ui"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestTUIModelShowsRunDAGCurrentActivityAndTokenTotal(t *testing.T) {
	t.Parallel()

	runDirectory := testRunDirectory(t, "run-42")
	planned, err := plan.NewForRun("root", runDirectory, []plan.NodeSpec{
		{Address: "research.static.collect", Kind: "research"},
		{Address: "research.static.summary", Kind: "research", Dependencies: []string{"research.static.collect"}},
	}, nil, nil, nil)
	require.NoError(t, err)
	projector := ui.NewProjector(planned)
	projector.Observe(debuglog.Event{
		Kind: debuglog.EventLifecycle, Action: "block.apply", Status: debuglog.StatusStarted,
		BlockAddress: "research.static.collect", BlockType: "research", Sequence: 1,
	})
	projector.Observe(debuglog.Event{
		Kind: debuglog.EventMessage, Action: "assistant.reasoning_delta",
		BlockAddress: "research.static.collect", Session: debuglog.SessionResearch,
		Content: "checking source dates", Sequence: 2,
	})
	projector.Observe(debuglog.Event{
		Kind: debuglog.EventMessage, Action: "assistant.usage",
		BlockAddress: "research.static.collect", Session: debuglog.SessionResearch,
		Usage: &debuglog.Usage{APICallID: "call-1", InputTokens: 100, OutputTokens: 25}, Sequence: 3,
	})
	model := resizeTUI(t, ui.NewTUIModel(projector, nil), 140, 32)

	view := model.View()

	assert.Contains(t, view, "Run: "+runDirectory)
	assert.Contains(t, view, "Tasks: 0/2 done")
	assert.Contains(t, view, "Tokens: 125")
	assert.Contains(t, view, "DAG *")
	assert.Contains(t, view, "research.static.collect")
	assert.Contains(t, view, "Activity: thinking")
	assert.Contains(t, view, "checking source dates")
}

func TestTUIModelAdjustsResearchTotalForMaterializedDynamicTasks(t *testing.T) {
	t.Parallel()
	runDirectory := testRunDirectory(t, "run-42")

	planned, err := plan.NewForRun("root", runDirectory, []plan.NodeSpec{
		{Address: "research.dynamic.followups", Kind: "research"},
		{Address: "research.static.summary", Kind: "research"},
	}, nil, nil, nil)
	require.NoError(t, err)
	projector := ui.NewProjector(planned)
	projector.Observe(debuglog.Event{
		Kind: debuglog.EventLifecycle, Action: "dynamic.tasks.materialized",
		Status: debuglog.StatusCompleted, BlockAddress: "research.dynamic.followups",
		BlockType: "research", Count: 2,
		Paths: []string{
			"research.dynamic.followups.tasks[0]",
			"research.dynamic.followups.tasks[1]",
		},
	})
	model := resizeTUI(t, ui.NewTUIModel(projector, nil), 140, 32)

	view := model.View()

	assert.Contains(t, view, "Tasks: 0/3 done")
	assert.Equal(t, 2, strings.Count(view, "research.dynamic.followups.tasks"))
}

func TestTUIModelNavigatesPanelsNodesAndLongContent(t *testing.T) {
	t.Parallel()

	projector := newTUIProjector(t)
	longContent := "begin-" + strings.Repeat("0123456789", 20) + "-end"
	projector.Observe(debuglog.Event{
		Kind: debuglog.EventMessage, Action: "assistant.message_delta",
		BlockAddress: "research.static.collect", Session: debuglog.SessionResearch,
		Content: longContent, Sequence: 1,
	})
	model := resizeTUI(t, ui.NewTUIModel(projector, nil), 70, 20)

	model = updateTUI(t, model, tea.KeyMsg{Type: tea.KeyRight})
	assert.Contains(t, model.View(), "Detail *")
	assert.Contains(t, model.View(), "begin-")
	detail := model.View()
	model = updateTUI(t, model, tea.KeyMsg{Type: tea.KeyRight, Alt: true})
	model = updateTUI(t, model, tea.KeyMsg{Type: tea.KeyRight, Alt: true})
	assert.Equal(t, detail, model.View())

	model = updateTUI(t, model, tea.KeyMsg{Type: tea.KeyLeft})
	model = updateTUI(t, model, tea.KeyMsg{Type: tea.KeyDown})
	model = updateTUI(t, model, tea.KeyMsg{Type: tea.KeyRight})
	assert.Contains(t, model.View(), "Address: research.static.summary")
}

func TestTUIModelWrapsStreamingContentWithinDetailPanel(t *testing.T) {
	t.Parallel()

	projector := newTUIProjector(t)
	projector.Observe(debuglog.Event{
		Kind: debuglog.EventMessage, Action: "assistant.message_delta", MessageID: "message-1",
		BlockAddress: "research.static.collect", Session: debuglog.SessionResearch,
		Content: "start " + strings.Repeat("wrapped content ", 12) + "tail-marker", Sequence: 1,
	})
	model := resizeTUI(t, ui.NewTUIModel(projector, nil), 120, 24)
	model = updateTUI(t, model, tea.KeyMsg{Type: tea.KeyRight})

	view := model.View()
	assert.Contains(t, view, "start wrapped")
	assert.Contains(t, view, "tail-marker")

	model = updateTUI(t, model, tea.KeyMsg{Type: tea.KeyRight, Alt: true})
	assert.Equal(t, view, model.View())

	model = resizeTUI(t, model, 70, 24)
	resized := model.View()
	assert.Contains(t, resized, "start wrapped")
	assert.Contains(t, resized, "tail-marker")
	assertViewFitsTerminal(t, resized, 70, 24)
}

func TestTUIModelCollapsesModuleAndConfirmsCancellation(t *testing.T) {
	t.Parallel()
	runDirectory := testRunDirectory(t, "run-module")

	child, err := plan.NewWithContextAndLocals("child", []plan.NodeSpec{
		{Address: "research.static.child", Kind: "research"},
	}, nil, nil, nil)
	require.NoError(t, err)
	planned, err := plan.NewForRun("root", runDirectory, []plan.NodeSpec{
		{Address: "module.sources", Kind: "module", Module: &plan.ModuleSpec{Plan: child}},
		{Address: "research.static.summary", Kind: "research", Dependencies: []string{"module.sources"}},
	}, nil, nil, nil)
	require.NoError(t, err)
	cancelled := false
	model := resizeTUI(t, ui.NewTUIModel(ui.NewProjector(planned), func() { cancelled = true }), 90, 24)
	require.Contains(t, model.View(), "module.sources.research.static.child")

	model = updateTUI(t, model, tea.KeyMsg{Type: tea.KeyEnter})
	assert.NotContains(t, model.View(), "module.sources.research.static.child")
	next, firstQuit := model.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'q'}})
	updated, ok := next.(ui.TUIModel)
	require.True(t, ok)
	model = updated
	assert.Nil(t, firstQuit)
	assert.Contains(t, model.View(), "Press q again to cancel")
	next, secondQuit := model.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'q'}})
	_ = next
	require.NotNil(t, secondQuit)
	assert.IsType(t, tea.QuitMsg{}, secondQuit())
	assert.True(t, cancelled)
}

func TestTUIModelCtrlCImmediatelyCancels(t *testing.T) {
	t.Parallel()

	cancelled := false
	model := ui.NewTUIModel(newTUIProjector(t), func() { cancelled = true })

	_, command := model.Update(tea.KeyMsg{Type: tea.KeyCtrlC})

	require.NotNil(t, command)
	assert.IsType(t, tea.QuitMsg{}, command())
	assert.True(t, cancelled)
}

func TestRunTUIExecutesApplyAndExitsWhenTheRunCompletes(t *testing.T) {
	t.Parallel()

	projector := newTUIProjector(t)
	ctx, cancel := context.WithCancel(t.Context())
	t.Cleanup(cancel)
	applyCalled := false
	var output bytes.Buffer

	err := ui.RunTUI(ctx, strings.NewReader(""), &output, projector, cancel, func() error {
		applyCalled = true
		projector.Observe(debuglog.Event{
			Kind: debuglog.EventLifecycle, Action: "block.apply", Status: debuglog.StatusCompleted,
			BlockAddress: "research.static.collect", BlockType: "research", Sequence: 1,
		})
		return nil
	})

	require.NoError(t, err)
	assert.True(t, applyCalled)
	assert.Equal(t, 1, projector.Snapshot().Research.Completed)
}

func TestTUIModelKeepsSelectedNodeVisibleInLongDAG(t *testing.T) {
	t.Parallel()
	runDirectory := testRunDirectory(t, "run-long")

	nodes := make([]plan.NodeSpec, 20)
	for index := range nodes {
		nodes[index] = plan.NodeSpec{
			Address: fmt.Sprintf("research.static.task_%02d", index+1), Kind: "research",
		}
	}
	planned, err := plan.NewForRun("root", runDirectory, nodes, nil, nil, nil)
	require.NoError(t, err)
	model := resizeTUI(t, ui.NewTUIModel(ui.NewProjector(planned), nil), 70, 12)

	model = updateTUI(t, model, tea.KeyMsg{Type: tea.KeyEnd})
	view := model.View()

	assert.Contains(t, view, "> [ ] research.static.task_20")
	assert.NotContains(t, view, "research.static.task_01")
}

func TestTUIModelReflowsAcrossWindowResizeEvents(t *testing.T) {
	t.Parallel()

	model := ui.NewTUIModel(newTUIProjector(t), nil)
	model = resizeTUI(t, model, 140, 30)
	wide := model.View()
	assert.Contains(t, wide, "DAG *")
	assert.Contains(t, wide, "Detail")
	assert.Contains(t, wide, "Timeline")
	assertViewFitsTerminal(t, wide, 140, 30)

	model = updateTUI(t, model, tea.KeyMsg{Type: tea.KeyRight})
	model = resizeTUI(t, model, 70, 14)
	narrow := model.View()
	assert.Contains(t, narrow, "Detail *")
	assert.NotContains(t, narrow, "DAG *")
	assert.NotContains(t, narrow, "Timeline")
	assertViewFitsTerminal(t, narrow, 70, 14)

	model = resizeTUI(t, model, 110, 20)
	wideAgain := model.View()
	assert.Contains(t, wideAgain, "DAG")
	assert.Contains(t, wideAgain, "Detail *")
	assert.Contains(t, wideAgain, "Timeline")
	assertViewFitsTerminal(t, wideAgain, 110, 20)
}

func TestTUIModelSanitizesPlanKindInDetail(t *testing.T) {
	t.Parallel()

	planned, err := plan.NewWithContextAndLocals("root", []plan.NodeSpec{
		{Address: "custom.bad", Kind: "research\x1b[2Jforged\a"},
	}, nil, nil, nil)
	require.NoError(t, err)
	model := resizeTUI(t, ui.NewTUIModel(ui.NewProjector(planned), nil), 70, 20)
	model = updateTUI(t, model, tea.KeyMsg{Type: tea.KeyRight})

	view := model.View()
	assert.NotContains(t, view, "\x1b[2J")
	assert.NotContains(t, view, "\a")
	assert.Contains(t, view, "Kind: researchforged")
}

func TestTUIModelRequestsFullRedrawForEveryWindowResize(t *testing.T) {
	t.Parallel()

	model := ui.NewTUIModel(newTUIProjector(t), nil)
	for _, size := range []tea.WindowSizeMsg{
		{Width: 50, Height: 12},
		{Width: 99, Height: 18},
		{Width: 100, Height: 18},
		{Width: 160, Height: 40},
	} {
		next, command := model.Update(size)
		updated, ok := next.(ui.TUIModel)
		require.True(t, ok)
		require.NotNil(t, command)
		model = updated
		assertViewFitsTerminal(t, model.View(), size.Width, size.Height)
	}
}

func TestTUIModelClampsPanelScrollAfterWindowResize(t *testing.T) {
	t.Parallel()

	projector := newTUIProjector(t)
	projector.Observe(debuglog.Event{
		Kind: debuglog.EventMessage, Action: "assistant.message",
		BlockAddress: "research.static.collect", Session: debuglog.SessionResearch,
		Content: strings.Repeat("detail line\n", 10), Sequence: 1,
	})
	model := resizeTUI(t, ui.NewTUIModel(projector, nil), 70, 12)
	model = updateTUI(t, model, tea.KeyMsg{Type: tea.KeyRight})
	model = updateTUI(t, model, tea.KeyMsg{Type: tea.KeyEnd})
	assert.NotContains(t, model.View(), "Address: research.static.collect")

	model = resizeTUI(t, model, 70, 30)
	assert.Contains(t, model.View(), "Address: research.static.collect")
}

func TestTUIModelRewrapsDetailAfterWindowResize(t *testing.T) {
	t.Parallel()

	projector := newTUIProjector(t)
	projector.Observe(debuglog.Event{
		Kind: debuglog.EventMessage, Action: "assistant.message",
		BlockAddress: "research.static.collect", Session: debuglog.SessionResearch,
		Content: "begin-" + strings.Repeat("x", 48) + "-end", Sequence: 1,
	})
	model := resizeTUI(t, ui.NewTUIModel(projector, nil), 100, 20)
	model = updateTUI(t, model, tea.KeyMsg{Type: tea.KeyRight})
	detail := model.View()
	for range 5 {
		model = updateTUI(t, model, tea.KeyMsg{Type: tea.KeyRight, Alt: true})
	}
	assert.Equal(t, detail, model.View())

	model = resizeTUI(t, model, 90, 20)
	assert.Contains(t, model.View(), "begin-")
}

func TestTUIModelReportsModuleFailureInOverallStatus(t *testing.T) {
	t.Parallel()
	runDirectory := testRunDirectory(t, "run-module")

	child, err := plan.NewWithContextAndLocals("child", []plan.NodeSpec{
		{Address: "research.static.child", Kind: "research"},
	}, nil, nil, nil)
	require.NoError(t, err)
	planned, err := plan.NewForRun("root", runDirectory, []plan.NodeSpec{
		{Address: "module.sources", Kind: "module", Module: &plan.ModuleSpec{Plan: child}},
	}, nil, nil, nil)
	require.NoError(t, err)
	projector := ui.NewProjector(planned)
	projector.Observe(debuglog.Event{
		Kind: debuglog.EventLifecycle, Action: "block.factory", Status: debuglog.StatusFailed,
		BlockAddress: "module.sources", BlockType: "module", Sequence: 1,
	})

	view := resizeTUI(t, ui.NewTUIModel(projector, nil), 90, 20).View()

	assert.Contains(t, view, "r42 apply | FAILED")
	assert.Contains(t, view, "Tasks: 0/1 done")
	assert.Contains(t, view, "Failed: 1")
}

func newTUIProjector(t *testing.T) *ui.Projector {
	t.Helper()
	runDirectory := testRunDirectory(t, "run-42")
	planned, err := plan.NewForRun("root", runDirectory, []plan.NodeSpec{
		{Address: "research.static.collect", Kind: "research"},
		{Address: "research.static.summary", Kind: "research", Dependencies: []string{"research.static.collect"}},
	}, nil, nil, nil)
	require.NoError(t, err)
	return ui.NewProjector(planned)
}

func resizeTUI(t *testing.T, model ui.TUIModel, width, height int) ui.TUIModel {
	t.Helper()
	return updateTUI(t, model, tea.WindowSizeMsg{Width: width, Height: height})
}

func updateTUI(t *testing.T, model ui.TUIModel, message tea.Msg) ui.TUIModel {
	t.Helper()
	next, _ := model.Update(message)
	actual, ok := next.(ui.TUIModel)
	require.True(t, ok)
	return actual
}

func assertViewFitsTerminal(t *testing.T, view string, width, height int) {
	t.Helper()
	lines := strings.Split(view, "\n")
	assert.LessOrEqual(t, len(lines), height)
	for index, line := range lines {
		assert.LessOrEqualf(t, ansi.StringWidth(line), width, "line %d exceeds resized width", index+1)
	}
}
