// Package progress implements the JSONL machine progress protocol between
// r42 and a supervising worker process. It defines the handshake frames, the
// common event envelope, the schema-1 projection records, and the
// schema-specific encoders used by the `r42 apply --ui=jsonl` contract.
//
// The protocol never serializes internal debuglog.Event values directly. Only
// the allowlisted projection record types defined in this package may be
// encoded, and each record is validated before encoding.
package progress

import (
	"fmt"
	"strings"
	"time"
	"unicode"
)

const (
	// HandshakeVersion is the stable version of the hello/select/ready
	// handshake, independent of event schema majors.
	HandshakeVersion = 1

	// Protocol identifies the JSONL progress protocol on the wire.
	Protocol = "r42.progress"

	// SchemaMajor1 is the first released event schema major.
	SchemaMajor1 = 1

	// R42Version is advertised in the hello frame. r42 currently has no
	// linker-injected version; the protocol example pins this value.
	R42Version = "0.8.0"
)

// Status is the lifecycle status of one projected node or of the whole run.
type Status string

const (
	StatusWaiting   Status = "waiting"
	StatusRunning   Status = "running"
	StatusSucceeded Status = "succeeded"
	StatusFailed    Status = "failed"
	StatusCanceled  Status = "canceled"
)

func validStatus(value Status) bool {
	switch value {
	case StatusWaiting, StatusRunning, StatusSucceeded, StatusFailed, StatusCanceled:
		return true
	default:
		return false
	}
}

// Phase is the workflow phase of one projected node.
type Phase string

const (
	PhaseCollection   Phase = "collection"
	PhaseCollectionQC Phase = "collection_qc"
	PhaseResearch     Phase = "research"
	PhaseQC           Phase = "qc"
	PhaseFinalQC      Phase = "final_qc"
	PhaseRevision     Phase = "revision"
)

func validPhase(value Phase) bool {
	switch value {
	case PhaseCollection, PhaseCollectionQC, PhaseResearch, PhaseQC, PhaseFinalQC, PhaseRevision:
		return true
	default:
		return false
	}
}

// Activity is the coarse per-node activity shown to a consumer.
type Activity string

const (
	ActivityIdle     Activity = "idle"
	ActivityThinking Activity = "thinking"
	ActivityReplying Activity = "replying"
	ActivityTool     Activity = "tool"
)

func validActivity(value Activity) bool {
	switch value {
	case ActivityIdle, ActivityThinking, ActivityReplying, ActivityTool:
		return true
	default:
		return false
	}
}

// Envelope carries the fields common to every post-ready event record. The
// type, critical, protocol, and schema_version fields are owned by the
// encoder for the negotiated schema major.
type Envelope struct {
	RunID     string
	Sequence  uint64
	Timestamp time.Time
}

func validSafeName(value string) bool {
	for _, char := range value {
		if unicode.IsSpace(char) || unicode.IsControl(char) {
			return false
		}
	}
	return value != ""
}

func validateEnvelope(envelope Envelope) error {
	if strings.TrimSpace(envelope.RunID) == "" {
		return fmt.Errorf("run_id is required")
	}
	return nil
}
