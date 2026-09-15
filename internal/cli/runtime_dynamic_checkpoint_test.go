package cli

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/lonegunmanb/r42/internal/checkpoint"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/zclconf/go-cty/cty"
)

func TestDynamicMaterializationStoreRestoresTasksWithoutReevaluation(t *testing.T) {
	t.Parallel()

	runDirectory := t.TempDir()
	store := checkpoint.NewStore(runDirectory)
	tasks := cty.TupleVal([]cty.Value{cty.ObjectVal(map[string]cty.Value{"topic": cty.StringVal("energy")})})
	require.NoError(t, saveDynamicTasks(store, tasks))

	restored, err := loadDynamicTasks(store)

	require.NoError(t, err)
	assert.True(t, tasks.RawEquals(restored))
}

func TestLoadOrEvaluateDynamicTasksEvaluatesWhenResumeHasNoCheckpoint(t *testing.T) {
	t.Parallel()

	store := checkpoint.NewStore(t.TempDir())
	want := cty.TupleVal([]cty.Value{cty.StringVal("first task")})
	calls := 0

	got, err := loadOrEvaluateDynamicTasks(store, true, func() (cty.Value, error) {
		calls++
		return want, nil
	})

	require.NoError(t, err)
	assert.Equal(t, 1, calls)
	assert.True(t, want.RawEquals(got))
	loaded, err := loadDynamicTasks(store)
	require.NoError(t, err)
	assert.True(t, want.RawEquals(loaded))
}

func TestLoadOrEvaluateDynamicTasksDoesNotMaskInvalidCheckpoint(t *testing.T) {
	t.Parallel()

	runDirectory := t.TempDir()
	store := checkpoint.NewStore(runDirectory)
	seed := cty.TupleVal([]cty.Value{cty.StringVal("saved task")})
	require.NoError(t, saveDynamicTasks(store, seed))
	root := filepath.Join(runDirectory, "checkpoints")
	latest, err := os.ReadFile(filepath.Join(root, "latest"))
	require.NoError(t, err)
	require.NoError(t, os.WriteFile(filepath.Join(root, string(latest[:len(latest)-1]), "manifest.json"), []byte("invalid"), 0o600))
	called := false
	_, err = loadOrEvaluateDynamicTasks(store, true, func() (cty.Value, error) {
		called = true
		return cty.StringVal("unexpected"), nil
	})

	require.Error(t, err)
	require.NotErrorIs(t, err, os.ErrNotExist)
	assert.False(t, called)
}

func TestLoadDynamicTasksRejectsPreUnitCheckpointFormat(t *testing.T) {
	t.Parallel()

	store := checkpoint.NewStore(t.TempDir())
	payload := []byte(`{"type":"[\"string\"]","value":"[\"saved task\"]"}`)
	require.NoError(t, store.Commit(checkpoint.State{Payload: payload}))

	_, err := loadDynamicTasks(store)

	require.ErrorContains(t, err, "unsupported dynamic tasks checkpoint version 0")
}
