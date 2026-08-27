package progress

import (
	"encoding/json"
	"slices"
	"strings"
	"sync"
	"unicode"
	"unicode/utf8"

	"github.com/charmbracelet/x/ansi"
	"github.com/lonegunmanb/r42/internal/debuglog"
	"github.com/lonegunmanb/r42/internal/plan"
)

// maxSummaryBytes bounds every sanitized summary to the schema-1 wire limit.
const maxSummaryBytes = 4096

// Snapshot is the current sanitized projection of the expanded DAG. Its node
// map can reproduce the TUI's DAG/detail selection without the complete Plan.
type Snapshot struct {
	Nodes []NodeProjection
}

// Projector builds the initial sanitized DAG snapshot from a Plan and tracks
// per-node phase, status, activity, safe tool name, and deduplicated aggregate
// token usage as debuglog events arrive. Its state is linear in the number of
// known blocks: unknown addresses are ignored and each node retains only its
// latest usage call ID.
//
// The projector is the privacy boundary of the JSONL protocol. It never copies
// prompts, variable values, tool arguments/results, process streams, SDK
// payloads, or raw debug content into a projection; the only text it exposes
// is the sanitized, bounded assistant-derived summary.
type Projector struct {
	mu    sync.RWMutex
	order []string
	nodes map[string]*NodeProjection
	// dynamicChildren maps a materialized parent address to its canonical
	// dynamic task addresses. It is bounded by the number of known blocks.
	dynamicChildren map[string][]string
}

// NewProjector builds the initial sanitized DAG snapshot from the fully
// expanded saved Plan, including nested module child Plans.
func NewProjector(planned *plan.Plan) *Projector {
	projector := &Projector{
		nodes:           make(map[string]*NodeProjection),
		dynamicChildren: make(map[string][]string),
	}
	if planned == nil {
		return projector
	}
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
		p.nodes[address] = &NodeProjection{
			BlockAddress:  address,
			BlockKind:     spec.Kind,
			ParentAddress: parent,
			Dependencies:  dependencies,
			Status:        StatusWaiting,
			Activity:      ActivityIdle,
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

// Observe updates the projection for the node addressed by the event. Events
// for unknown addresses are ignored so the node map stays linear in known
// blocks. Only allowlisted fields are copied into the projection.
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
	switch event.Action {
	case "block.apply":
		switch event.Status {
		case debuglog.StatusStarted:
			node.Status = StatusRunning
		case debuglog.StatusCompleted:
			node.Status = StatusSucceeded
			node.Activity = ActivityIdle
		case debuglog.StatusFailed:
			node.Status = StatusFailed
			node.Activity = ActivityIdle
		}
	case "block.factory":
		if event.Status == debuglog.StatusFailed {
			node.Status = StatusFailed
			node.Activity = ActivityIdle
		}
	}
	node.Phase = phaseFromSession(event.Session)
	switch event.Action {
	case "assistant.reasoning", "assistant.reasoning_delta":
		node.Activity = ActivityThinking
		node.summary = sanitizeSummary(event.Content)
	case "assistant.message", "assistant.message_delta":
		node.Activity = ActivityReplying
		node.summary = sanitizeSummary(event.Content)
	case "tool.execution_start", "tool.execution_progress", "tool.execution_partial_result":
		node.Activity = ActivityTool
		node.ToolName = sanitizeName(event.ToolName)
		node.summary = toolSummary("Running", node.ToolName)
	case "tool.execution_complete":
		node.Activity = ActivityThinking
		node.ToolName = sanitizeName(event.ToolName)
		node.summary = toolSummary("Finished", node.ToolName)
	case "assistant.usage":
		p.addUsage(node, event.Usage)
	}
}

func phaseFromSession(session debuglog.SessionKind) Phase {
	switch session {
	case debuglog.SessionCollection:
		return PhaseCollection
	case debuglog.SessionCollectionQC:
		return PhaseCollectionQC
	case debuglog.SessionResearch:
		return PhaseResearch
	case debuglog.SessionQC:
		return PhaseQC
	case debuglog.SessionFinalQC:
		return PhaseFinalQC
	case debuglog.SessionRevision:
		return PhaseRevision
	default:
		return ""
	}
}

func (p *Projector) addUsage(node *NodeProjection, usage *debuglog.Usage) {
	if usage == nil {
		return
	}
	if usage.APICallID != "" {
		if usage.APICallID == node.lastUsageCallID {
			return
		}
		node.lastUsageCallID = usage.APICallID
	}
	if node.Usage == nil {
		node.Usage = &TokenUsage{}
	}
	node.Usage.Input += usage.InputTokens
	node.Usage.Output += usage.OutputTokens
	node.Usage.Reasoning += usage.ReasoningTokens
	node.Usage.CacheRead += usage.CacheReadTokens
	node.Usage.CacheWrite += usage.CacheWriteTokens
}

// materializeDynamicTasks adds the canonical dynamic task addresses announced
// by a dynamic.tasks.materialized event as research nodes under the parent.
// Re-announced task sets are ignored so the node map does not grow.
func (p *Projector) materializeDynamicTasks(event debuglog.Event) {
	parent := event.BlockAddress
	if _, seen := p.dynamicChildren[parent]; seen {
		return
	}
	if p.nodes[parent] == nil {
		return
	}
	children := make([]string, 0, len(event.Paths))
	for _, address := range event.Paths {
		if !canonicalDynamicTaskAddress(parent, address) {
			continue
		}
		if _, exists := p.nodes[address]; exists {
			continue
		}
		p.order = append(p.order, address)
		p.nodes[address] = &NodeProjection{
			BlockAddress:  address,
			BlockKind:     "research",
			ParentAddress: parent,
			Status:        StatusWaiting,
			Activity:      ActivityIdle,
		}
		children = append(children, address)
	}
	p.dynamicChildren[parent] = children
}

func canonicalDynamicTaskAddress(parent, address string) bool {
	index, ok := strings.CutPrefix(address, parent+".tasks[")
	if !ok || !strings.HasSuffix(index, "]") {
		return false
	}
	index = strings.TrimSuffix(index, "]")
	if index == "" || index == "0" {
		return index == "0"
	}
	if index[0] < '1' || index[0] > '9' {
		return false
	}
	for _, char := range index[1:] {
		if char < '0' || char > '9' {
			return false
		}
	}
	return true
}

// Timeline returns the current per-block activity summary for one canonical
// address. It represents the latest timeline-append payload for that block and
// is derived from the retained projection, so it never accumulates history. It
// returns nil for unknown blocks and for blocks that have not yet produced any
// assistant-derived summary text, so callers naturally skip empty-summary
// timeline records (TimelineRecord requires a non-empty summary on the wire).
func (p *Projector) Timeline(address string) *TimelineRecord {
	p.mu.RLock()
	defer p.mu.RUnlock()
	node := p.nodes[address]
	if node == nil {
		return nil
	}
	summary := node.Summary()
	if summary == "" {
		return nil
	}
	return &TimelineRecord{
		BlockAddress: address,
		Activity:     node.Activity,
		Summary:      summary,
	}
}

// SnapshotRecord returns the current full DAG as a run_snapshot record. The
// record is rebuilt on demand so the projector never retains a second copy.
func (p *Projector) SnapshotRecord() *RunSnapshotRecord {
	return &RunSnapshotRecord{Nodes: p.Snapshot().Nodes}
}

// Materialized returns the dynamic_tasks_materialized record for one parent
// address: the newly announced dynamic children under it. Static or unknown
// parents yield an empty node list.
func (p *Projector) Materialized(parent string) *DynamicTasksMaterializedRecord {
	p.mu.RLock()
	defer p.mu.RUnlock()
	record := &DynamicTasksMaterializedRecord{
		ParentAddress: parent,
		Nodes:         make([]NodeProjection, 0, len(p.dynamicChildren[parent])),
	}
	for _, address := range p.dynamicChildren[parent] {
		node := p.nodes[address]
		if node == nil {
			continue
		}
		record.Nodes = append(record.Nodes, cloneProjection(node))
	}
	return record
}

// Snapshot returns a deep-enough copy of the current node projections so the
// caller cannot mutate projector state through the returned values.
func (p *Projector) Snapshot() Snapshot {
	p.mu.RLock()
	defer p.mu.RUnlock()
	snapshot := Snapshot{Nodes: make([]NodeProjection, 0, len(p.order))}
	for _, address := range p.order {
		node, ok := p.nodes[address]
		if !ok {
			continue
		}
		snapshot.Nodes = append(snapshot.Nodes, cloneProjection(node))
	}
	return snapshot
}

// MustNode returns the current projection for one canonical address, panicking
// when the address is not part of the expanded DAG.
func (p *Projector) MustNode(address string) NodeProjection {
	p.mu.RLock()
	defer p.mu.RUnlock()
	node, ok := p.nodes[address]
	if !ok {
		panic("progress node not found: " + address)
	}
	return cloneProjection(node)
}

// Node returns the current projection for one canonical address, reporting
// whether the address is part of the expanded DAG. It never panics.
func (p *Projector) Node(address string) (NodeProjection, bool) {
	p.mu.RLock()
	defer p.mu.RUnlock()
	node, ok := p.nodes[address]
	if !ok {
		return NodeProjection{}, false
	}
	return cloneProjection(node), true
}

func cloneProjection(node *NodeProjection) NodeProjection {
	cloned := *node
	cloned.Dependencies = slices.Clone(node.Dependencies)
	if node.Usage != nil {
		usage := *node.Usage
		cloned.Usage = &usage
	}
	return cloned
}

// Summary returns the sanitized, bounded assistant-derived summary of the
// node. It is the only free-text that may cross the privacy boundary.
func (n *NodeProjection) Summary() string {
	if n == nil {
		return ""
	}
	return n.summary
}

// String renders the wire projection for diagnostics and for tests proving
// sensitive fields never reach the encoded form.
func (n *NodeProjection) String() string {
	if n == nil {
		return "<nil>"
	}
	encoded, err := json.Marshal(n)
	if err != nil {
		return n.BlockAddress
	}
	return string(encoded)
}

func sanitizeName(value string) string {
	return strings.Map(func(char rune) rune {
		if unicode.IsSpace(char) {
			return -1
		}
		return char
	}, terminalText(value))
}

func toolSummary(verb, name string) string {
	if name == "" {
		name = "tool"
	}
	return sanitizeSummary(verb + " " + name)
}

func sanitizeSummary(value string) string {
	return truncateUTF8(terminalText(value), maxSummaryBytes)
}

// terminalText strips ANSI control sequences and other control characters,
// mirroring the display-side sanitization used by the TUI.
func terminalText(value string) string {
	value = ansi.Strip(value)
	return strings.Map(func(char rune) rune {
		if char == '\n' || char == '\t' || !unicode.IsControl(char) {
			return char
		}
		return -1
	}, value)
}

// truncateUTF8 shortens value to at most maxBytes bytes without cutting a
// multi-byte rune in half.
func truncateUTF8(value string, maxBytes int) string {
	if len(value) <= maxBytes {
		return value
	}
	truncated := value[:maxBytes]
	for len(truncated) > 0 && !utf8.ValidString(truncated) {
		truncated = truncated[:len(truncated)-1]
	}
	return truncated
}
