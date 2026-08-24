package workflow

import (
	"sync"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestStateMachineValidTransitions(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name          string
		start         Phase
		event         Event
		expected      Phase
		consumesRound bool
	}{
		{name: "initial collection round", start: PhaseCollection, event: EventCollectionCheckpoint, expected: PhaseCollectionQC, consumesRound: false},
		{name: "sufficient advances to research", start: PhaseCollectionQC, event: EventSufficient, expected: PhaseResearch, consumesRound: false},
		{name: "needs more resumes collection", start: PhaseCollectionQC, event: EventNeedsMore, expected: PhaseCollection, consumesRound: true},
		{name: "research completes to final qc", start: PhaseResearch, event: EventResearchComplete, expected: PhaseFinalQC, consumesRound: false},
		{name: "revise research returns to research", start: PhaseFinalQC, event: EventReviseResearch, expected: PhaseResearch, consumesRound: false},
		{name: "reopen collection starts next round", start: PhaseFinalQC, event: EventReopenCollection, expected: PhaseCollection, consumesRound: true},
		{name: "final pass completes", start: PhaseFinalQC, event: EventPass, expected: PhaseComplete, consumesRound: false},
		{name: "collection limit exhausted advances to research", start: PhaseCollectionQC, event: EventCollectionLimitExhausted, expected: PhaseResearch, consumesRound: false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			state := New(Config{MaxCollectionRounds: nil})
			require.NoError(t, state.Begin())
			state.phase = tt.start
			err := state.Advance(tt.event)
			require.NoError(t, err)
			assert.Equal(t, tt.expected, state.phase)
			if tt.consumesRound {
				assert.Equal(t, 2, state.CollectionRoundsUsed())
			} else {
				assert.Equal(t, 1, state.CollectionRoundsUsed())
			}
		})
	}
}

func TestStateMachineForbiddenTransitions(t *testing.T) {
	t.Parallel()

	transitions := []struct {
		from  Phase
		event Event
	}{
		{from: PhaseCollection, event: EventSufficient},
		{from: PhaseCollection, event: EventPass},
		{from: PhaseCollection, event: EventReviseResearch},
		{from: PhaseCollectionQC, event: EventCollectionCheckpoint},
		{from: PhaseCollectionQC, event: EventReopenCollection},
		{from: PhaseCollectionQC, event: EventPass},
		{from: PhaseResearch, event: EventCollectionCheckpoint},
		{from: PhaseResearch, event: EventNeedsMore},
		{from: PhaseResearch, event: EventPass},
		{from: PhaseResearch, event: EventReopenCollection},
		{from: PhaseFinalQC, event: EventCollectionCheckpoint},
		{from: PhaseFinalQC, event: EventSufficient},
		{from: PhaseFinalQC, event: EventNeedsMore},
		{from: PhaseComplete, event: EventPass},
	}

	for _, tt := range transitions {
		t.Run(tt.from.String()+"+"+tt.event.String(), func(t *testing.T) {
			t.Parallel()

			state := New(Config{MaxCollectionRounds: nil})
			require.NoError(t, state.Begin())
			state.phase = tt.from
			err := state.Advance(tt.event)
			require.Error(t, err)
			var transitionErr *TransitionError
			require.ErrorAs(t, err, &transitionErr)
			assert.Equal(t, tt.from, transitionErr.From)
			assert.Equal(t, tt.event, transitionErr.Event)
			assert.Equal(t, tt.from, state.phase)
		})
	}
}

func TestCollectionRoundsExhausted(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name      string
		maxRounds *int
		events    []Event
	}{
		{
			name:      "unlimited rounds allow multiple needs more",
			maxRounds: nil,
			events:    []Event{EventCollectionCheckpoint, EventNeedsMore, EventCollectionCheckpoint, EventNeedsMore},
		},
		{
			name:      "needs more beyond configured limit fails",
			maxRounds: intPointer(2),
			events:    []Event{EventCollectionCheckpoint, EventNeedsMore},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			state := New(Config{MaxCollectionRounds: tt.maxRounds})
			require.NoError(t, state.Begin())
			for _, event := range tt.events {
				require.NoError(t, state.Advance(event))
			}
			require.NoError(t, state.Advance(EventCollectionCheckpoint))
			err := state.Advance(EventNeedsMore)
			if tt.maxRounds == nil {
				require.NoError(t, err)
				return
			}
			require.Error(t, err)
			var budgetErr *BudgetExhaustedError
			require.ErrorAs(t, err, &budgetErr)
			assert.Equal(t, PhaseCollectionQC, state.phase)
		})
	}
}

func TestCollectionLimitExhaustedDuringReview(t *testing.T) {
	t.Parallel()

	state := New(Config{MaxCollectionRounds: intPointer(2)})
	require.NoError(t, state.Begin())
	require.NoError(t, state.Advance(EventCollectionCheckpoint))
	require.NoError(t, state.Advance(EventNeedsMore)) // round 2
	require.NoError(t, state.Advance(EventCollectionCheckpoint))
	require.NoError(t, state.Advance(EventCollectionLimitExhausted))
	assert.Equal(t, PhaseResearch, state.phase)
	assert.Equal(t, 2, state.CollectionRoundsUsed())
}

func TestCollectorCanPermanentlyExhaustUnlimitedCollection(t *testing.T) {
	t.Parallel()

	state := New(Config{})
	require.NoError(t, state.Begin())
	require.NoError(t, state.MarkCollectionExhausted())
	assert.True(t, state.CollectionLimitExhausted())
	require.NoError(t, state.Advance(EventCollectionCheckpoint))
	require.NoError(t, state.Advance(EventCollectionLimitExhausted))
	require.NoError(t, state.Advance(EventResearchComplete))

	err := state.Advance(EventReopenCollection)

	require.Error(t, err)
	var budgetErr *BudgetExhaustedError
	require.ErrorAs(t, err, &budgetErr)
	assert.Equal(t, PhaseFinalQC, state.Phase())
}

func TestMarkCollectionExhaustedRejectsInvalidPhases(t *testing.T) {
	t.Parallel()

	t.Run("before workflow begins", func(t *testing.T) {
		t.Parallel()

		err := New(Config{}).MarkCollectionExhausted()

		assert.ErrorContains(t, err, "workflow must begin")
	})

	t.Run("outside collection", func(t *testing.T) {
		t.Parallel()

		state := New(Config{})
		require.NoError(t, state.Begin())
		require.NoError(t, state.Advance(EventCollectionCheckpoint))

		err := state.MarkCollectionExhausted()

		var transitionErr *TransitionError
		require.ErrorAs(t, err, &transitionErr)
		assert.Equal(t, PhaseCollectionQC, state.Phase())
	})
}

func TestFinalQCReturnPaths(t *testing.T) {
	t.Parallel()

	t.Run("reopen collection consumes round", func(t *testing.T) {
		t.Parallel()

		state := New(Config{MaxCollectionRounds: intPointer(3)})
		require.NoError(t, state.Begin())
		require.NoError(t, state.Advance(EventCollectionCheckpoint))
		require.NoError(t, state.Advance(EventSufficient))
		require.NoError(t, state.Advance(EventResearchComplete))
		require.NoError(t, state.Advance(EventReopenCollection))
		assert.Equal(t, PhaseCollection, state.phase)
		assert.Equal(t, 2, state.CollectionRoundsUsed())
	})

	t.Run("exhausted budget rejects reopen and keeps final qc", func(t *testing.T) {
		t.Parallel()

		state := New(Config{MaxCollectionRounds: intPointer(1)})
		require.NoError(t, state.Begin())
		require.NoError(t, state.Advance(EventCollectionCheckpoint))
		require.NoError(t, state.Advance(EventSufficient))
		require.NoError(t, state.Advance(EventResearchComplete))
		err := state.Advance(EventReopenCollection)
		require.Error(t, err)
		var budgetErr *BudgetExhaustedError
		require.ErrorAs(t, err, &budgetErr)
		assert.Equal(t, PhaseFinalQC, state.phase)
		assert.Equal(t, 1, state.CollectionRoundsUsed())
	})

	t.Run("revise research does not consume round", func(t *testing.T) {
		t.Parallel()

		state := New(Config{MaxCollectionRounds: intPointer(3)})
		require.NoError(t, state.Begin())
		require.NoError(t, state.Advance(EventCollectionCheckpoint))
		require.NoError(t, state.Advance(EventSufficient))
		require.NoError(t, state.Advance(EventResearchComplete))
		require.NoError(t, state.Advance(EventReviseResearch))
		assert.Equal(t, PhaseResearch, state.phase)
		assert.Equal(t, 1, state.CollectionRoundsUsed())
	})
}

func TestResearchCompletesWithoutFinalQC(t *testing.T) {
	t.Parallel()

	state := New(Config{})
	require.NoError(t, state.Begin())
	require.NoError(t, state.Advance(EventCollectionCheckpoint))
	require.NoError(t, state.Advance(EventSufficient))
	require.NoError(t, state.Advance(EventResearchCompleteWithoutQC))
	assert.Equal(t, PhaseComplete, state.Phase())
}

func TestMaxCollectionRoundsReturnsDefensiveCopy(t *testing.T) {
	t.Parallel()

	maximum := 3
	state := New(Config{MaxCollectionRounds: &maximum})
	first := state.MaxCollectionRounds()
	require.NotNil(t, first)
	*first = 99

	assert.Equal(t, 3, *state.MaxCollectionRounds())
	assert.Nil(t, New(Config{}).MaxCollectionRounds())
}

func TestCollectionRoundsUsedCountsOnlyAcquisitionTransitions(t *testing.T) {
	t.Parallel()

	state := New(Config{MaxCollectionRounds: nil})
	require.NoError(t, state.Begin())
	require.NoError(t, state.Advance(EventCollectionCheckpoint))
	require.NoError(t, state.Advance(EventSufficient))
	require.NoError(t, state.Advance(EventResearchComplete))
	require.NoError(t, state.Advance(EventReviseResearch))
	require.NoError(t, state.Advance(EventResearchComplete))
	require.NoError(t, state.Advance(EventReviseResearch))
	assert.Equal(t, 1, state.CollectionRoundsUsed())
}

func TestCheckpointPending(t *testing.T) {
	t.Parallel()

	t.Run("absent limit leaves no pending", func(t *testing.T) {
		t.Parallel()

		state := New(Config{MaxCollectionRounds: nil})
		require.NoError(t, state.Begin())
		require.False(t, state.CheckpointPending())
	})

	t.Run("reaching batch enters pending", func(t *testing.T) {
		t.Parallel()

		state := New(Config{MaxCollectionRounds: nil})
		require.NoError(t, state.Begin())
		for range 10 {
			require.NoError(t, state.RegisterEvidenceArtifact())
		}
		require.True(t, state.CheckpointPending())
		require.ErrorContains(t, state.AcquireGate(), "checkpoint pending")
	})

	t.Run("checkpoint clears pending and evidence artifact count", func(t *testing.T) {
		t.Parallel()

		state := New(Config{MaxCollectionRounds: nil})
		require.NoError(t, state.Begin())
		for range 10 {
			require.NoError(t, state.RegisterEvidenceArtifact())
		}
		require.NoError(t, state.Checkpoint())
		require.False(t, state.CheckpointPending())
	})

	t.Run("needs more resets evidence artifact count", func(t *testing.T) {
		t.Parallel()

		state := New(Config{MaxCollectionRounds: nil})
		require.NoError(t, state.Begin())
		for range 3 {
			require.NoError(t, state.RegisterEvidenceArtifact())
		}
		require.NoError(t, state.Checkpoint())
		require.NoError(t, state.Advance(EventCollectionCheckpoint))
		require.NoError(t, state.Advance(EventNeedsMore))
		require.Equal(t, 0, state.UnreviewedEvidenceArtifactCount())
	})

	t.Run("early checkpoint allowed below batch", func(t *testing.T) {
		t.Parallel()

		state := New(Config{MaxCollectionRounds: nil})
		require.NoError(t, state.Begin())
		for range 2 {
			require.NoError(t, state.RegisterEvidenceArtifact())
		}
		require.False(t, state.CheckpointPending())
		require.NoError(t, state.Checkpoint())
	})

	t.Run("zero batch size defaults to ten", func(t *testing.T) {
		t.Parallel()

		state := New(Config{MaxCollectionRounds: nil, BatchSize: 0})
		require.NoError(t, state.Begin())
		require.False(t, state.CheckpointPending())
		for range 9 {
			require.NoError(t, state.RegisterEvidenceArtifact())
		}
		require.False(t, state.CheckpointPending())
		require.NoError(t, state.RegisterEvidenceArtifact())
		require.True(t, state.CheckpointPending())
	})
}

func TestEvidenceArtifactCountAndCursor(t *testing.T) {
	t.Parallel()

	state := New(Config{MaxCollectionRounds: nil, BatchSize: 2})
	require.NoError(t, state.Begin())
	require.NoError(t, state.RegisterEvidenceArtifact())
	require.NoError(t, state.RegisterEvidenceArtifact())
	require.Equal(t, 2, state.UnreviewedEvidenceArtifactCount())
	require.NoError(t, state.Checkpoint())
	require.Equal(t, 0, state.Cursor())
	require.NoError(t, state.Advance(EventCollectionCheckpoint))
	require.NoError(t, state.Advance(EventNeedsMore))
	require.Equal(t, 1, state.Cursor())
	require.NoError(t, state.RegisterEvidenceArtifact())
	require.Equal(t, 1, state.UnreviewedEvidenceArtifactCount())
	require.NoError(t, state.Checkpoint())
	require.NoError(t, state.Advance(EventCollectionCheckpoint))
	require.NoError(t, state.Advance(EventSufficient))
	require.Equal(t, 2, state.Cursor())
}

func TestCheckpointWithoutPendingRegistrationsRequiresReason(t *testing.T) {
	t.Parallel()

	state := New(Config{MaxCollectionRounds: nil})
	require.NoError(t, state.Begin())
	err := state.Checkpoint()
	require.Error(t, err)
	assert.ErrorContains(t, err, "empty checkpoint requires a reason")
}

func TestAcquireGateOnlyRejectsNewAcquisition(t *testing.T) {
	t.Parallel()

	state := New(Config{MaxCollectionRounds: nil})
	require.NoError(t, state.Begin())
	require.NoError(t, state.AcquireGate())
	require.NoError(t, state.RegisterEvidenceArtifact())
	require.NoError(t, state.RegisterEvidenceArtifact())
	for range 8 {
		require.NoError(t, state.RegisterEvidenceArtifact())
	}
	require.True(t, state.CheckpointPending())
	require.ErrorContains(t, state.AcquireGate(), "checkpoint pending")
	require.NoError(t, state.RegisterEvidenceArtifact())
	require.NoError(t, state.Checkpoint())
}

func TestBeginValidatesConfig(t *testing.T) {
	t.Parallel()

	state := New(Config{MaxCollectionRounds: intPointer(0)})
	require.Error(t, state.Begin())
	require.ErrorContains(t, state.Advance(EventCollectionCheckpoint), "must begin")
}

func TestStateConcurrentRegistration(t *testing.T) {
	t.Parallel()

	state := New(Config{BatchSize: 100})
	require.NoError(t, state.Begin())
	var group sync.WaitGroup
	errors := make(chan error, 100)
	for range 100 {
		group.Go(func() { errors <- state.RegisterEvidenceArtifact() })
	}
	group.Wait()
	close(errors)
	for err := range errors {
		require.NoError(t, err)
	}

	assert.Equal(t, 100, state.UnreviewedEvidenceArtifactCount())
	assert.True(t, state.CheckpointPending())
}

func TestInvalidConfig(t *testing.T) {
	t.Parallel()

	state := New(Config{MaxCollectionRounds: intPointer(-1)})
	require.Error(t, state.Begin())
}

func intPointer(value int) *int { return &value }
