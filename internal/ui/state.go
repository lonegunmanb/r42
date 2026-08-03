package ui

import (
	"slices"
	"strings"
	"sync"
	"unicode"

	"github.com/charmbracelet/x/ansi"
	"github.com/lonegunmanb/r42/internal/debuglog"
	"github.com/lonegunmanb/r42/internal/plan"
)

type Status string

const (
	StatusWaiting   Status = "waiting"
	StatusRunning   Status = "running"
	StatusCompleted Status = "completed"
	StatusFailed    Status = "failed"
	StatusSkipped   Status = "skipped"
)

type Activity string

const (
	ActivityIdle     Activity = "idle"
	ActivityThinking Activity = "thinking"
	ActivityReplying Activity = "replying"
	ActivityTool     Activity = "tool"
)

type ResearchCounts struct {
	Total     int
	Running   int
	Completed int
	Failed    int
}

type Node struct {
	Address      string
	Kind         string
	ResearchTask bool
	Parent       string
	Dependencies []string
	Status       Status
	Phase        debuglog.SessionKind
	Activity     Activity
	Content      string
	ToolName     string
	Usage        debuglog.Usage
	LastSequence uint64
	contentID    string
	contentKind  string
	contentPhase debuglog.SessionKind
}

type TimelineEntry struct {
	Address    string
	Session    debuglog.SessionKind
	Activity   Activity
	Content    string
	ToolName   string
	Action     string
	Error      string
	MessageID  string
	ToolCallID string
}

type Snapshot struct {
	RunDirectory string
	Nodes        []Node
	Research     ResearchCounts
	Usage        debuglog.Usage
	Timeline     []TimelineEntry
}

func (s Snapshot) Node(address string) (Node, bool) {
	for _, node := range s.Nodes {
		if node.Address == address {
			return node, true
		}
	}
	return Node{}, false
}

func (s Snapshot) MustNode(address string) Node {
	node, ok := s.Node(address)
	if !ok {
		panic("run node not found: " + address)
	}
	return node
}

type Projector struct {
	mu           sync.RWMutex
	runDirectory string
	order        []string
	nodes        map[string]*Node
	usage        debuglog.Usage
	seenCalls    map[string]struct{}
	timeline     []TimelineEntry
}

func NewProjector(planned *plan.Plan) *Projector {
	projector := &Projector{
		nodes: make(map[string]*Node), seenCalls: make(map[string]struct{}),
	}
	if planned == nil {
		return projector
	}
	projector.runDirectory = terminalText(planned.RunDirectory())
	projector.addPlan(planned, "", "")
	return projector
}

func (p *Projector) addPlan(planned *plan.Plan, prefix, parent string) {
	for _, spec := range planned.Nodes() {
		address := canonicalAddress(prefix, spec.Address)
		dependencies := make([]string, len(spec.Dependencies))
		for index, dependency := range spec.Dependencies {
			dependencies[index] = canonicalAddress(prefix, dependency)
		}
		p.order = append(p.order, address)
		p.nodes[address] = &Node{
			Address: address, Kind: spec.Kind, Parent: parent,
			ResearchTask: spec.Kind == "research",
			Dependencies: dependencies, Status: StatusWaiting, Activity: ActivityIdle,
		}
		if spec.Module != nil && spec.Module.Plan != nil {
			p.addPlan(spec.Module.Plan, address, address)
		}
	}
}

func canonicalAddress(prefix, address string) string {
	if prefix == "" {
		return address
	}
	return prefix + "." + address
}

func (p *Projector) Observe(event debuglog.Event) {
	p.mu.Lock()
	defer p.mu.Unlock()
	if event.Action == "dynamic.tasks.materialized" {
		p.materializeDynamicTasks(event)
	}
	node := p.nodes[event.BlockAddress]
	if node == nil {
		return
	}
	node.LastSequence = event.Sequence
	content := terminalText(event.Content)
	toolName := terminalText(event.ToolName)
	if event.Action == "block.apply" {
		switch event.Status {
		case debuglog.StatusStarted:
			node.Status = StatusRunning
		case debuglog.StatusCompleted:
			node.Status = StatusCompleted
			node.Activity = ActivityIdle
		case debuglog.StatusFailed:
			node.Status = StatusFailed
			node.Activity = ActivityIdle
		case debuglog.StatusSkipped:
			node.Status = StatusSkipped
		}
	}
	if event.Action == "block.factory" && event.Status == debuglog.StatusFailed {
		node.Status = StatusFailed
		node.Activity = ActivityIdle
	}
	node.Phase = event.Session
	switch event.Action {
	case "assistant.reasoning", "assistant.reasoning_delta":
		node.Activity = ActivityThinking
		p.prepareContent(node, event, "reasoning")
		if event.Action == "assistant.reasoning" {
			node.Content = content
		} else {
			node.Content = appendContent(node.Content, content)
		}
	case "assistant.message", "assistant.message_delta":
		node.Activity = ActivityReplying
		p.prepareContent(node, event, "message")
		if event.Action == "assistant.message" {
			node.Content = content
		} else {
			node.Content = appendContent(node.Content, content)
		}
	case "tool.execution_start", "tool.execution_progress", "tool.execution_partial_result":
		node.Activity = ActivityTool
		node.ToolName = toolName
	case "tool.execution_complete":
		node.Activity = ActivityThinking
		node.ToolName = toolName
	case "assistant.usage":
		p.addUsage(node, event.Usage)
	}
	p.updateTimeline(event, node.Activity, content, toolName)
}

func (p *Projector) materializeDynamicTasks(event debuglog.Event) {
	parent := p.nodes[event.BlockAddress]
	if parent == nil || parent.Kind != "research" || !parent.ResearchTask {
		return
	}
	parent.ResearchTask = false
	children := make([]string, 0, len(event.Paths))
	for _, address := range event.Paths {
		if address == "" {
			continue
		}
		if _, exists := p.nodes[address]; exists {
			continue
		}
		p.nodes[address] = &Node{
			Address: address, Kind: "research", ResearchTask: true, Parent: event.BlockAddress,
			Status: StatusWaiting, Activity: ActivityIdle,
		}
		children = append(children, address)
	}
	if len(children) == 0 {
		return
	}
	parentIndex := slices.Index(p.order, event.BlockAddress)
	if parentIndex < 0 {
		p.order = append(p.order, children...)
		return
	}
	p.order = slices.Insert(p.order, parentIndex+1, children...)
}

func (p *Projector) prepareContent(node *Node, event debuglog.Event, streamKind string) {
	if node.contentKind != streamKind || node.contentPhase != event.Session ||
		(event.MessageID != "" && node.contentID != event.MessageID) {
		node.Content = ""
	}
	node.contentKind = streamKind
	node.contentPhase = event.Session
	if event.MessageID != "" {
		node.contentID = event.MessageID
	}
}

func (p *Projector) updateTimeline(
	event debuglog.Event,
	activity Activity,
	content string,
	toolName string,
) {
	if content == "" && len(event.Arguments) > 0 {
		content = terminalText(string(event.Arguments))
	}
	if content == "" && len(event.Result) > 0 {
		content = terminalText(string(event.Result))
	}
	errorText := terminalText(event.Error)
	if content == "" && toolName == "" && errorText == "" {
		return
	}
	for index := range slices.Backward(p.timeline) {
		entry := &p.timeline[index]
		if entry.Address != event.BlockAddress || entry.Session != event.Session {
			continue
		}
		if event.MessageID != "" && entry.MessageID == event.MessageID &&
			timelineStream(entry.Action) == timelineStream(event.Action) {
			entry.Action = event.Action
			entry.Activity = activity
			if strings.HasSuffix(event.Action, "_delta") {
				entry.Content = appendContent(entry.Content, content)
			} else {
				entry.Content = content
			}
			entry.Error = errorText
			return
		}
		if event.ToolCallID != "" && entry.ToolCallID == event.ToolCallID {
			entry.Action = event.Action
			entry.Activity = activity
			entry.ToolName = toolName
			entry.Content = appendTimelineContent(entry.Content, content)
			entry.Error = errorText
			return
		}
	}
	p.timeline = append(p.timeline, TimelineEntry{
		Address: event.BlockAddress, Session: event.Session, Activity: activity,
		Content: content, ToolName: toolName, Action: event.Action, Error: errorText,
		MessageID: event.MessageID, ToolCallID: event.ToolCallID,
	})
}

func timelineStream(action string) string {
	switch {
	case strings.Contains(action, "reasoning"):
		return "reasoning"
	case strings.Contains(action, "message"):
		return "message"
	default:
		return ""
	}
}

func appendTimelineContent(current, next string) string {
	if next == "" || strings.Contains(current, next) {
		return current
	}
	if current == "" {
		return next
	}
	return current + "\n" + next
}

func terminalText(value string) string {
	value = ansi.Strip(value)
	return strings.Map(func(char rune) rune {
		if char == '\n' || char == '\t' || !unicode.IsControl(char) {
			return char
		}
		return -1
	}, value)
}

func appendContent(current, delta string) string {
	if delta == "" {
		return current
	}
	if strings.HasSuffix(current, delta) {
		return current
	}
	return current + delta
}

func (p *Projector) addUsage(node *Node, usage *debuglog.Usage) {
	if usage == nil {
		return
	}
	if usage.APICallID != "" {
		if _, seen := p.seenCalls[usage.APICallID]; seen {
			return
		}
		p.seenCalls[usage.APICallID] = struct{}{}
	}
	addUsage(&p.usage, *usage)
	addUsage(&node.Usage, *usage)
}

func addUsage(total *debuglog.Usage, delta debuglog.Usage) {
	total.InputTokens += delta.InputTokens
	total.OutputTokens += delta.OutputTokens
	total.ReasoningTokens += delta.ReasoningTokens
	total.CacheReadTokens += delta.CacheReadTokens
	total.CacheWriteTokens += delta.CacheWriteTokens
}

func (p *Projector) Snapshot() Snapshot {
	p.mu.RLock()
	defer p.mu.RUnlock()
	snapshot := Snapshot{
		RunDirectory: p.runDirectory, Nodes: make([]Node, 0, len(p.order)),
		Usage: p.usage, Timeline: slices.Clone(p.timeline),
	}
	for _, address := range p.order {
		node := *p.nodes[address]
		node.Dependencies = slices.Clone(node.Dependencies)
		snapshot.Nodes = append(snapshot.Nodes, node)
		if !node.ResearchTask {
			continue
		}
		snapshot.Research.Total++
		switch node.Status {
		case StatusRunning:
			snapshot.Research.Running++
		case StatusCompleted:
			snapshot.Research.Completed++
		case StatusFailed:
			snapshot.Research.Failed++
		}
	}
	return snapshot
}
