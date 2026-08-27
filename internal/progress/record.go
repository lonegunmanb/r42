package progress

import "fmt"

// Record is one sanitized progress event that a schema encoder knows how to
// project. Only these allowlisted record types are accepted by the encoder
// API; raw debuglog.Event values are never serialized.
type Record interface {
	// Type is the wire record type, e.g. "node_upsert".
	Type() string
	// Critical marks a record that a consumer must understand; an unknown
	// critical record makes the consumer's progress view incomplete.
	Critical() bool
	// Validate rejects records with invalid or incomplete required fields
	// before any bytes are written.
	Validate() error
}

// TokenUsage is the deduplicated aggregate token usage for one node.
type TokenUsage struct {
	Input      int64 `json:"input,omitempty"`
	Output     int64 `json:"output,omitempty"`
	Reasoning  int64 `json:"reasoning,omitempty"`
	CacheRead  int64 `json:"cache_read,omitempty"`
	CacheWrite int64 `json:"cache_write,omitempty"`
}

func (u *TokenUsage) validate() error {
	if u == nil {
		return nil
	}
	if u.Input < 0 || u.Output < 0 || u.Reasoning < 0 || u.CacheRead < 0 || u.CacheWrite < 0 {
		return fmt.Errorf("usage tokens must be non-negative")
	}
	return nil
}

// NodeProjection is the self-contained, sanitized projection of one canonical
// block address. It is not a delta and does not carry prompts, variable
// values, tool arguments/results, or raw streams.
type NodeProjection struct {
	BlockAddress  string      `json:"block_address"`
	BlockKind     string      `json:"block_kind,omitempty"`
	ParentAddress string      `json:"parent_address,omitempty"`
	Dependencies  []string    `json:"dependencies,omitempty"`
	Phase         Phase       `json:"phase,omitempty"`
	Status        Status      `json:"status"`
	Activity      Activity    `json:"activity,omitempty"`
	ToolName      string      `json:"tool_name,omitempty"`
	Usage         *TokenUsage `json:"usage,omitempty"`

	// summary is the sanitized, bounded assistant-derived text for this node.
	// It is never serialized by the wire encoder; Summary() exposes it to the
	// projector's consumers for timeline records in later tasks.
	summary         string
	lastUsageCallID string
}

func (n *NodeProjection) validate() error {
	if n == nil {
		return fmt.Errorf("node projection is required")
	}
	if !validSafeName(n.BlockAddress) {
		return fmt.Errorf("block_address is required")
	}
	if !validSafeName(n.BlockKind) {
		return fmt.Errorf("block_kind is required")
	}
	if n.Phase != "" && !validPhase(n.Phase) {
		return fmt.Errorf("invalid phase %q", n.Phase)
	}
	if !validStatus(n.Status) {
		return fmt.Errorf("invalid status %q", n.Status)
	}
	if n.Activity != "" && !validActivity(n.Activity) {
		return fmt.Errorf("invalid activity %q", n.Activity)
	}
	if n.ToolName != "" && !validSafeName(n.ToolName) {
		return fmt.Errorf("invalid tool_name %q", n.ToolName)
	}
	return n.Usage.validate()
}

// RunSnapshotRecord is the initial sanitized projection of the expanded DAG.
// It is critical because consumers need it to construct their node map.
type RunSnapshotRecord struct {
	Nodes []NodeProjection `json:"nodes"`
}

// DynamicTasksMaterializedRecord announces nodes created by dynamic task
// materialization. It is critical because consumers need the new node set.
type DynamicTasksMaterializedRecord struct {
	ParentAddress string           `json:"parent_address"`
	Nodes         []NodeProjection `json:"nodes"`
}

// NodeRecord replaces the current projection for one canonical block address.
// It is critical and self-contained rather than a delta.
type NodeRecord struct {
	Node NodeProjection `json:"node"`
}

// TimelineRecord appends one best-effort, human-readable progress item for a
// block. It is non-critical.
type TimelineRecord struct {
	BlockAddress string   `json:"block_address"`
	Activity     Activity `json:"activity,omitempty"`
	Summary      string   `json:"summary"`
}

// RunCompletedRecord reports successful completion. It never contains output
// values or report paths.
type RunCompletedRecord struct {
	Status    Status `json:"status"`
	Total     int    `json:"total"`
	Succeeded int    `json:"succeeded"`
	Failed    int    `json:"failed"`
}

// RunFailedRecord reports command failure after successful negotiation.
type RunFailedRecord struct {
	Status  Status `json:"status"`
	Summary string `json:"summary"`
}

// RunCanceledRecord reports cancellation observed by r42.
type RunCanceledRecord struct {
	Status  Status `json:"status"`
	Summary string `json:"summary"`
}

func (r *NodeRecord) Type() string { return "node_upsert" }

func (r *NodeRecord) Critical() bool { return true }

func (r *NodeRecord) Validate() error {
	return r.Node.validate()
}

func (r *RunSnapshotRecord) Type() string { return "run_snapshot" }

func (r *RunSnapshotRecord) Critical() bool { return true }

func (r *RunSnapshotRecord) Validate() error {
	return validateNodes(r.Nodes)
}

func (r *DynamicTasksMaterializedRecord) Type() string { return "dynamic_tasks_materialized" }

func (r *DynamicTasksMaterializedRecord) Critical() bool { return true }

func (r *DynamicTasksMaterializedRecord) Validate() error {
	if !validSafeName(r.ParentAddress) {
		return fmt.Errorf("parent_address is required")
	}
	return validateNodes(r.Nodes)
}

func (r *TimelineRecord) Type() string { return "timeline_append" }

func (r *TimelineRecord) Critical() bool { return false }

func (r *TimelineRecord) Validate() error {
	if !validSafeName(r.BlockAddress) {
		return fmt.Errorf("block_address is required")
	}
	if r.Activity != "" && !validActivity(r.Activity) {
		return fmt.Errorf("invalid activity %q", r.Activity)
	}
	if r.Summary == "" {
		return fmt.Errorf("summary is required")
	}
	return nil
}

func (r *RunCompletedRecord) Type() string { return "run_completed" }

func (r *RunCompletedRecord) Critical() bool { return true }

func (r *RunCompletedRecord) Validate() error {
	if r.Status != StatusSucceeded {
		return fmt.Errorf("run_completed status must be %q, got %q", StatusSucceeded, r.Status)
	}
	if r.Total < 0 || r.Succeeded < 0 || r.Failed < 0 {
		return fmt.Errorf("run counts must be non-negative")
	}
	if r.Failed != 0 || r.Succeeded != r.Total {
		return fmt.Errorf("run_completed counts must contain only %d succeeded nodes, got %d succeeded and %d failed", r.Total, r.Succeeded, r.Failed)
	}
	return nil
}

func (r *RunFailedRecord) Type() string { return "run_failed" }

func (r *RunFailedRecord) Critical() bool { return true }

func (r *RunFailedRecord) Validate() error {
	if r.Status != StatusFailed {
		return fmt.Errorf("run_failed status must be %q, got %q", StatusFailed, r.Status)
	}
	if r.Summary == "" {
		return fmt.Errorf("summary is required")
	}
	return nil
}

func (r *RunCanceledRecord) Type() string { return "run_canceled" }

func (r *RunCanceledRecord) Critical() bool { return true }

func (r *RunCanceledRecord) Validate() error {
	if r.Status != StatusCanceled {
		return fmt.Errorf("run_canceled status must be %q, got %q", StatusCanceled, r.Status)
	}
	if r.Summary == "" {
		return fmt.Errorf("summary is required")
	}
	return nil
}

func validateNodes(nodes []NodeProjection) error {
	if nodes == nil {
		return fmt.Errorf("nodes are required")
	}
	for index := range nodes {
		if err := nodes[index].validate(); err != nil {
			return fmt.Errorf("nodes[%d]: %w", index, err)
		}
	}
	return nil
}
