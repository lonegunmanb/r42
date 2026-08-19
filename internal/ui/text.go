package ui

import (
	"fmt"
	"io"
	"strings"
	"sync"

	"github.com/lonegunmanb/r42/internal/debuglog"
)

type TextRenderer struct {
	mu        sync.Mutex
	writer    io.Writer
	projector *Projector
	announced map[string]Activity
}

func NewTextRenderer(writer io.Writer, projector *Projector) *TextRenderer {
	return &TextRenderer{writer: writer, projector: projector, announced: make(map[string]Activity)}
}

func (r *TextRenderer) Start() error {
	r.mu.Lock()
	defer r.mu.Unlock()
	snapshot := r.projector.Snapshot()
	_, err := fmt.Fprintf(r.writer, "Run: %s\nResearch tasks: %d\n\n%s\n",
		terminalText(snapshot.RunDirectory), snapshot.Research.Total, RenderDAG(snapshot))
	return err
}

func (r *TextRenderer) Observe(event debuglog.Event) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.projector.Observe(event)
	node, ok := r.projector.Snapshot().Node(event.BlockAddress)
	if !ok {
		return
	}
	if event.Action == "dynamic.tasks.materialized" {
		_, _ = fmt.Fprintf(r.writer, "[dynamic] MATERIALIZED %s tasks=%d\n",
			terminalText(event.BlockAddress), event.Count)
		return
	}
	if node.Kind == "module" {
		address := terminalText(event.BlockAddress)
		if event.Action == "block.apply" ||
			event.Action == "block.factory" && event.Status == debuglog.StatusFailed {
			switch event.Status {
			case debuglog.StatusStarted:
				_, _ = fmt.Fprintf(r.writer, "[module] START %s\n", address)
			case debuglog.StatusCompleted:
				_, _ = fmt.Fprintf(r.writer, "[module] DONE %s\n", address)
			case debuglog.StatusFailed:
				_, _ = fmt.Fprintf(r.writer, "[module] FAILED %s %s\n", address, terminalText(event.Error))
			}
		}
		return
	}
	if node.Kind != "research" {
		return
	}
	prefix := "[dynamic]"
	if node.ResearchTask {
		ordinal, total := r.researchOrdinal(event.BlockAddress)
		prefix = fmt.Sprintf("[%d/%d]", ordinal, total)
	}
	if event.Action == "block.apply" {
		switch event.Status {
		case debuglog.StatusStarted:
			_, _ = fmt.Fprintf(r.writer, "%s START %s\n", prefix, terminalText(event.BlockAddress))
		case debuglog.StatusCompleted:
			_, _ = fmt.Fprintf(r.writer, "%s DONE %s tokens=%d\n", prefix, terminalText(event.BlockAddress), node.Usage.TotalTokens())
		case debuglog.StatusFailed:
			_, _ = fmt.Fprintf(r.writer, "%s FAILED %s %s\n", prefix, terminalText(event.BlockAddress), terminalText(event.Error))
		}
		return
	}
	if event.Action == "block.factory" && event.Status == debuglog.StatusFailed {
		_, _ = fmt.Fprintf(r.writer, "%s FAILED %s %s\n", prefix,
			terminalText(event.BlockAddress), terminalText(event.Error))
		return
	}
	phase := terminalText(string(event.Session))
	if phase == "" {
		phase = "research"
	}
	if event.Action == "workflow.phase" {
		decision := ""
		if event.Content != "" {
			decision = " decision=" + terminalText(event.Content)
		}
		_, _ = fmt.Fprintf(r.writer, "%s[%s] PHASE round=%d%s\n", prefix, phase, event.Count, decision)
		return
	}
	switch event.Action {
	case "assistant.reasoning", "assistant.reasoning_delta":
		if r.announced[event.BlockAddress] != ActivityThinking {
			_, _ = fmt.Fprintf(r.writer, "%s[%s] THINKING %s\n", prefix, phase, terminalText(event.Content))
			r.announced[event.BlockAddress] = ActivityThinking
		}
	case "assistant.message", "assistant.message_delta":
		if r.announced[event.BlockAddress] != ActivityReplying {
			_, _ = fmt.Fprintf(r.writer, "%s[%s] REPLYING %s\n", prefix, phase, terminalText(event.Content))
			r.announced[event.BlockAddress] = ActivityReplying
		}
	case "tool.execution_start":
		_, _ = fmt.Fprintf(r.writer, "%s[%s] TOOL %s\n", prefix, phase, terminalText(event.ToolName))
		r.announced[event.BlockAddress] = ActivityTool
	}
}

func (r *TextRenderer) researchOrdinal(address string) (int, int) {
	ordinal := 0
	total := 0
	for _, node := range r.projector.Snapshot().Nodes {
		if !node.ResearchTask {
			continue
		}
		total++
		if node.Address == address {
			ordinal = total
		}
	}
	return ordinal, total
}

func RenderDAG(snapshot Snapshot) string {
	lines := make([]string, 0, len(snapshot.Nodes))
	for _, node := range snapshot.Nodes {
		if len(node.Dependencies) == 0 {
			lines = append(lines, terminalText(node.Address))
			continue
		}
		for _, dependency := range node.Dependencies {
			lines = append(lines, terminalText(dependency)+" -> "+terminalText(node.Address))
		}
	}
	return strings.Join(lines, "\n")
}
