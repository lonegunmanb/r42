// Package workflow owns the pure research workflow state machine. It is
// independent of SDK sessions so every phase and budget transition can be
// exhaustively tested without network access.
package workflow

import (
	"errors"
	"fmt"
)

// Phase is one state in the research workflow state machine.
type Phase string

const (
	PhaseCollection   Phase = "collection"
	PhaseCollectionQC Phase = "collection_qc"
	PhaseResearch     Phase = "research"
	PhaseFinalQC      Phase = "final_qc"
	PhaseComplete     Phase = "complete"
)

func (p Phase) String() string { return string(p) }

// Event is a transition trigger between phases.
type Event string

const (
	EventCollectionCheckpoint     Event = "checkpoint"
	EventSufficient               Event = "sufficient"
	EventNeedsMore                Event = "needs_more"
	EventCollectionLimitExhausted Event = "collection_limit_exhausted"
	EventResearchComplete         Event = "research_complete"
	EventReviseResearch           Event = "revise_research"
	EventReopenCollection         Event = "reopen_collection"
	EventPass                     Event = "pass"
)

func (e Event) String() string { return string(e) }

// Config is the immutable workflow configuration. A nil MaxCollectionRounds
// means an unlimited collection budget. BatchSize defaults to 10 when zero.
type Config struct {
	MaxCollectionRounds *int
	BatchSize           int
}

const defaultBatchSize = 10

// State is the mutable runtime state of one research workflow instance. Each
// dynamic research member owns an isolated State instance.
type State struct {
	config Config

	phase                  Phase
	begun                  bool
	collectionRoundsUsed   int
	cursor                 int
	unreviewedCount        int
	checkpointPending      bool
	lastCollectionQCIssues []string
}

// New creates an unbegun workflow state.
func New(config Config) *State {
	if config.BatchSize == 0 {
		config.BatchSize = defaultBatchSize
	}
	return &State{config: config}
}

// TransitionError describes a forbidden phase transition. It is deterministic
// and repairable by the workflow driver.
type TransitionError struct {
	From  Phase
	Event Event
}

func (e *TransitionError) Error() string {
	return fmt.Sprintf("invalid transition %s from phase %s", e.Event, e.From)
}

// BudgetExhaustedError reports that the configured collection-round budget
// prevents another acquisition phase. Unlike TransitionError, the transition
// itself is legal; the budget vetoes it.
type BudgetExhaustedError struct {
	Phase Phase
	Event Event
}

func (e *BudgetExhaustedError) Error() string {
	return fmt.Sprintf("collection rounds exhausted for %s in phase %s", e.Event, e.Phase)
}

// Begin starts the workflow in the initial Collection phase (round 1).
func (s *State) Begin() error {
	if s.config.MaxCollectionRounds != nil && *s.config.MaxCollectionRounds <= 0 {
		return errors.New("max collection rounds must be positive")
	}
	if s.config.BatchSize <= 0 {
		return errors.New("collection batch size must be positive")
	}
	if s.begun {
		return errors.New("workflow already begun")
	}
	s.phase = PhaseCollection
	s.collectionRoundsUsed = 1
	s.begun = true
	return nil
}

// Phase returns the current phase, or an empty string before Begin.
func (s *State) Phase() Phase {
	if !s.begun {
		return ""
	}
	return s.phase
}

// CollectionRoundsUsed returns the number of acquisition rounds consumed,
// including the initial Collection phase.
func (s *State) CollectionRoundsUsed() int { return s.collectionRoundsUsed }

// Cursor returns the number of snapshots that have received a valid
// Collection-QC verdict. It advances only on valid verdicts.
func (s *State) Cursor() int { return s.cursor }

// UnreviewedSnapshotCount returns snapshots registered since the last
// checkpoint.
func (s *State) UnreviewedSnapshotCount() int { return s.unreviewedCount }

// CheckpointPending reports whether the unreviewed snapshot count reached the
// configured batch size.
func (s *State) CheckpointPending() bool { return s.checkpointPending }

// CollectionLimitExhausted reports whether the configured round budget prevents
// another acquisition phase.
func (s *State) CollectionLimitExhausted() bool {
	if s.config.MaxCollectionRounds == nil {
		return false
	}
	return s.collectionRoundsUsed >= *s.config.MaxCollectionRounds
}

// RegisterSnapshot records one newly registered unique snapshot. Reaching the
// configured batch size enters checkpoint_pending, after which new acquisition
// calls are rejected while in-flight completion, registration, and checkpoint
// remain available.
func (s *State) RegisterSnapshot() error {
	if !s.begun {
		return errors.New("workflow must begin before registering snapshots")
	}
	if s.config.BatchSize <= 0 {
		return errors.New("collection batch size must be positive")
	}
	s.unreviewedCount++
	if s.unreviewedCount >= s.config.BatchSize {
		s.checkpointPending = true
	}
	return nil
}

// Checkpoint submits every unreviewed snapshot for review and clears the
// pending batch state. An empty checkpoint requires a non-empty reason.
func (s *State) Checkpoint() error {
	if !s.begun {
		return errors.New("workflow must begin before checkpointing")
	}
	if s.unreviewedCount == 0 {
		return errors.New("empty checkpoint requires a reason")
	}
	s.unreviewedCount = 0
	s.checkpointPending = false
	return nil
}

// AcquireGate enforces the checkpoint_pending acquisition gate. It rejects new
// acquisition while allowing registration and checkpoint to proceed.
func (s *State) AcquireGate() error {
	if !s.begun {
		return errors.New("workflow must begin before acquiring")
	}
	if s.checkpointPending {
		return errors.New("checkpoint pending: submit a checkpoint before acquiring more sources")
	}
	return nil
}

// Advance applies one event to the state machine. Invalid transitions return a
// deterministic *TransitionError and leave the state unchanged.
func (s *State) Advance(event Event) error {
	if !s.begun {
		return errors.New("workflow must begin before advancing")
	}
	switch s.phase {
	case PhaseCollection:
		if event == EventCollectionCheckpoint {
			s.phase = PhaseCollectionQC
			return nil
		}
	case PhaseCollectionQC:
		switch event {
		case EventSufficient, EventCollectionLimitExhausted:
			s.phase = PhaseResearch
			s.cursor++
			return nil
		case EventNeedsMore:
			if s.config.MaxCollectionRounds != nil && s.collectionRoundsUsed >= *s.config.MaxCollectionRounds {
				return &BudgetExhaustedError{Phase: s.phase, Event: event}
			}
			s.phase = PhaseCollection
			s.collectionRoundsUsed++
			s.cursor++
			return nil
		}
	case PhaseResearch:
		if event == EventResearchComplete {
			s.phase = PhaseFinalQC
			return nil
		}
	case PhaseFinalQC:
		switch event {
		case EventPass:
			s.phase = PhaseComplete
			return nil
		case EventReviseResearch:
			s.phase = PhaseResearch
			return nil
		case EventReopenCollection:
			if s.config.MaxCollectionRounds != nil && s.collectionRoundsUsed >= *s.config.MaxCollectionRounds {
				return &BudgetExhaustedError{Phase: s.phase, Event: event}
			}
			s.phase = PhaseCollection
			s.collectionRoundsUsed++
			return nil
		}
	case PhaseComplete:
	}
	return &TransitionError{From: s.phase, Event: event}
}

// SetLastCollectionQCIssues records the most recent Collection-QC verdict
// issues for context handed to later phases.
func (s *State) SetLastCollectionQCIssues(issues []string) {
	s.lastCollectionQCIssues = append([]string(nil), issues...)
}

// LastCollectionQCIssues returns a copy of the most recent Collection-QC
// verdict issues.
func (s *State) LastCollectionQCIssues() []string {
	return append([]string(nil), s.lastCollectionQCIssues...)
}
