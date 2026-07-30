package executor_test

import (
	"context"
	"errors"
	"fmt"
	"io"
	"sync"
	"sync/atomic"
	"testing"

	"github.com/Azure/golden"
	r42concurrency "github.com/lonegunmanb/r42/internal/concurrency"
	"github.com/lonegunmanb/r42/internal/executor"
	"github.com/lonegunmanb/r42/internal/plan"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/zclconf/go-cty/cty"
	"go.uber.org/goleak"
)

func TestMain(m *testing.M) { goleak.VerifyTestMain(m) }

func TestExecutorAppliesGoldenBlocksInDependencyOrder(t *testing.T) {
	t.Parallel()

	events := &eventLog{}
	factory := &fakeFactory{build: func(_ context.Context, node plan.NodeSpec, _ *r42concurrency.Scope) (golden.ApplyBlock, error) {
		return newFakeBlock(node.Address, func() error {
			events.add("apply " + node.Address)
			return nil
		}, nil), nil
	}}
	planned := savedPlan(t, []plan.NodeSpec{
		{Address: "research.source", Kind: "research", Config: cty.EmptyObjectVal},
		{Address: "research.report", Kind: "research", Dependencies: []string{"research.source"}, Config: cty.EmptyObjectVal},
	}, map[string]plan.OutputSpec{"status": {Value: cty.StringVal("complete")}})
	runner := executor.New(factory, nil)

	outputs, err := runner.Apply(t.Context(), planned, 2)

	require.NoError(t, err)
	assert.Equal(t, []string{"apply research.source", "apply research.report"}, events.values())
	assert.Equal(t, cty.StringVal("complete"), outputs["status"])
	assert.Empty(t, runner.Warnings())
}

func TestExecutorCancelsRootAndCleansUpAfterActiveApplyStops(t *testing.T) {
	t.Parallel()

	wantErr := errors.New("primary apply failure")
	closeErr := errors.New("close session failed")
	debugErr := errors.New("flush debug failed")
	events := &eventLog{}
	bothStarted := make(chan struct{})
	var starts sync.WaitGroup
	starts.Add(2)
	factory := &fakeFactory{build: func(ctx context.Context, node plan.NodeSpec, _ *r42concurrency.Scope) (golden.ApplyBlock, error) {
		switch node.Address {
		case "research.failure":
			return newFakeBlock(node.Address, func() error {
				starts.Done()
				<-bothStarted
				events.add("apply failed")
				return wantErr
			}, func(context.Context) error {
				events.add("close failure session")
				return closeErr
			}), nil
		case "research.active":
			return newFakeBlock(node.Address, func() error {
				starts.Done()
				<-bothStarted
				<-ctx.Done()
				events.add("active tool stopped")
				return ctx.Err()
			}, func(context.Context) error {
				events.add("close active session")
				return nil
			}), nil
		default:
			return newFakeBlock(node.Address, func() error {
				events.add("unexpected dependent apply")
				return nil
			}, nil), nil
		}
	}}
	debug := &fakeCloser{close: func() error {
		events.add("flush debug")
		return debugErr
	}}
	planned := savedPlan(t, []plan.NodeSpec{
		{Address: "research.failure", Kind: "research", Config: cty.EmptyObjectVal},
		{Address: "research.active", Kind: "research", Config: cty.EmptyObjectVal},
		{Address: "research.dependent", Kind: "research", Dependencies: []string{"research.failure"}, Config: cty.EmptyObjectVal},
	}, nil)
	runner := executor.New(factory, debug)
	go func() {
		starts.Wait()
		close(bothStarted)
	}()

	_, err := runner.Apply(t.Context(), planned, 2)

	require.Error(t, err)
	require.ErrorIs(t, err, wantErr)
	require.NotErrorIs(t, err, context.Canceled)
	warnings := runner.Warnings()
	require.Len(t, warnings, 2)
	require.ErrorIs(t, warnings[0], closeErr)
	require.ErrorIs(t, warnings[1], debugErr)
	actual := events.values()
	assert.NotContains(t, actual, "unexpected dependent apply")
	assert.Less(t, eventIndex(t, actual, "active tool stopped"), eventIndex(t, actual, "close failure session"))
	assert.Less(t, eventIndex(t, actual, "active tool stopped"), eventIndex(t, actual, "close active session"))
	assert.Equal(t, len(actual)-1, eventIndex(t, actual, "flush debug"))
}

func TestExecutorCleanupUsesLiveContextAfterApplyFailure(t *testing.T) {
	t.Parallel()

	wantErr := errors.New("apply failed")
	cleanupContextErr := errors.New("cleanup received canceled context")
	closed := false
	factory := &fakeFactory{build: func(_ context.Context, node plan.NodeSpec, _ *r42concurrency.Scope) (golden.ApplyBlock, error) {
		return newFakeBlock(node.Address, func() error { return wantErr }, func(ctx context.Context) error {
			if ctx.Err() != nil {
				return cleanupContextErr
			}
			closed = true
			return nil
		}), nil
	}}
	runner := executor.New(factory, nil)
	planned := savedPlan(t, []plan.NodeSpec{{Address: "research.failure", Kind: "research", Config: cty.EmptyObjectVal}}, nil)

	_, err := runner.Apply(t.Context(), planned, 1)

	require.ErrorIs(t, err, wantErr)
	assert.True(t, closed)
	for _, warning := range runner.Warnings() {
		require.NotErrorIs(t, warning, cleanupContextErr)
	}
}

func TestExecutorWaitsForNestedApplyShutdownBeforeDebugFlush(t *testing.T) {
	t.Parallel()

	events := &eventLog{}
	nestedStopped := make(chan struct{})
	bothStarted := make(chan struct{})
	var starts sync.WaitGroup
	starts.Add(2)
	factory := &fakeFactory{build: func(ctx context.Context, node plan.NodeSpec, _ *r42concurrency.Scope) (golden.ApplyBlock, error) {
		if node.Address == "module.child" {
			return newFakeBlock(node.Address, func() error {
				starts.Done()
				<-bothStarted
				<-ctx.Done()
				events.add("nested stopped")
				close(nestedStopped)
				return ctx.Err()
			}, nil), nil
		}
		return newFakeBlock(node.Address, func() error {
			starts.Done()
			<-bothStarted
			return errors.New("root failed")
		}, nil), nil
	}}
	debug := &fakeCloser{close: func() error {
		select {
		case <-nestedStopped:
			events.add("debug closed")
			return nil
		default:
			return errors.New("debug closed before nested executor stopped")
		}
	}}
	planned := savedPlan(t, []plan.NodeSpec{
		{Address: "module.child", Kind: "module", Config: cty.EmptyObjectVal, Module: &plan.ModuleSpec{Plan: savedPlan(t, nil, nil)}},
		{Address: "research.failure", Kind: "research", Config: cty.EmptyObjectVal},
	}, nil)
	runner := executor.New(factory, debug)
	go func() {
		starts.Wait()
		close(bothStarted)
	}()

	_, err := runner.Apply(t.Context(), planned, 2)

	require.Error(t, err)
	assert.Equal(t, []string{"nested stopped", "debug closed"}, events.values())
	assert.Empty(t, runner.Warnings())
}

func TestExecutorDoesNotResumeCompletedNodes(t *testing.T) {
	t.Parallel()

	var applied int
	factory := &fakeFactory{build: func(_ context.Context, node plan.NodeSpec, _ *r42concurrency.Scope) (golden.ApplyBlock, error) {
		return newFakeBlock(node.Address, func() error { applied++; return nil }, nil), nil
	}}
	planned := savedPlan(t, []plan.NodeSpec{{Address: "research.once", Kind: "research", Config: cty.EmptyObjectVal}}, nil)
	runner := executor.New(factory, nil)

	_, firstErr := runner.Apply(t.Context(), planned, 1)
	_, secondErr := runner.Apply(t.Context(), planned, 1)

	require.NoError(t, firstErr)
	require.NoError(t, secondErr)
	assert.Equal(t, 2, applied)
}

func TestExecutorResearchPermitIncludesCleanup(t *testing.T) {
	t.Parallel()

	var active atomic.Int64
	var maximum atomic.Int64
	factory := &fakeFactory{build: func(_ context.Context, node plan.NodeSpec, _ *r42concurrency.Scope) (golden.ApplyBlock, error) {
		return newFakeBlock(node.Address, func() error {
			current := active.Add(1)
			for observed := maximum.Load(); current > observed && !maximum.CompareAndSwap(observed, current); observed = maximum.Load() {
			}
			return nil
		}, func(context.Context) error {
			active.Add(-1)
			return nil
		}), nil
	}}
	planned := savedPlan(t, []plan.NodeSpec{
		{Address: "research.first", Kind: "research", Config: cty.EmptyObjectVal},
		{Address: "research.second", Kind: "research", Config: cty.EmptyObjectVal},
	}, nil)
	runner := executor.New(factory, nil)

	_, err := runner.Apply(t.Context(), planned, 1)

	require.NoError(t, err)
	assert.Equal(t, int64(1), maximum.Load())
	assert.Zero(t, active.Load())
}

func TestExecutorCancelsResearchWaitingForPermitWithoutCreatingBlock(t *testing.T) {
	t.Parallel()

	wantErr := errors.New("first research failed")
	var factoryCalls atomic.Int64
	factory := &fakeFactory{build: func(_ context.Context, node plan.NodeSpec, _ *r42concurrency.Scope) (golden.ApplyBlock, error) {
		factoryCalls.Add(1)
		return newFakeBlock(node.Address, func() error { return wantErr }, nil), nil
	}}
	planned := savedPlan(t, []plan.NodeSpec{
		{Address: "research.one", Kind: "research", Config: cty.EmptyObjectVal},
		{Address: "research.two", Kind: "research", Config: cty.EmptyObjectVal},
	}, nil)
	runner := executor.New(factory, nil)

	_, err := runner.Apply(t.Context(), planned, 1)

	require.ErrorIs(t, err, wantErr)
	assert.Equal(t, int64(1), factoryCalls.Load())
}

func TestExecutorDoesNotStartModulesAfterParentCancellation(t *testing.T) {
	t.Parallel()

	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	created := false
	factory := &fakeFactory{build: func(context.Context, plan.NodeSpec, *r42concurrency.Scope) (golden.ApplyBlock, error) {
		created = true
		return newFakeBlock("module.child", func() error { return nil }, nil), nil
	}}
	planned := savedPlan(t, []plan.NodeSpec{{
		Address: "module.child",
		Kind:    "module",
		Config:  cty.EmptyObjectVal,
		Module:  &plan.ModuleSpec{Plan: savedPlan(t, nil, nil)},
	}}, nil)
	runner := executor.New(factory, nil)

	_, err := runner.Apply(ctx, planned, 1)

	require.ErrorIs(t, err, context.Canceled)
	assert.False(t, created)
}

func TestExecutorValidatesInputsAndFactoryFailures(t *testing.T) {
	t.Parallel()

	factoryErr := errors.New("factory failed")
	tests := []struct {
		name    string
		runner  *executor.Executor
		planned *plan.Plan
		limit   int
		want    string
	}{
		{name: "factory required", runner: executor.New(nil, nil), planned: savedPlan(t, nil, nil), want: "apply block factory is required"},
		{name: "plan required", runner: executor.New(&fakeFactory{}, nil), want: "saved plan is required"},
		{name: "parallelism validation", runner: executor.New(&fakeFactory{}, nil), planned: savedPlan(t, nil, nil), limit: -1, want: "global parallelism must not be negative"},
		{name: "factory error", runner: executor.New(&fakeFactory{err: factoryErr}, nil), planned: savedPlan(t, []plan.NodeSpec{{Address: "research.bad", Kind: "research", Config: cty.EmptyObjectVal}}, nil), want: "create apply block research.bad: factory failed"},
		{name: "nil block", runner: executor.New(&fakeFactory{}, nil), planned: savedPlan(t, []plan.NodeSpec{{Address: "research.bad", Kind: "research", Config: cty.EmptyObjectVal}}, nil), want: "create apply block research.bad: factory returned nil"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			_, err := tt.runner.Apply(t.Context(), tt.planned, tt.limit)
			require.EqualError(t, err, tt.want)
		})
	}
}

func TestApplyErrorExposesDefensiveCleanupWarnings(t *testing.T) {
	t.Parallel()

	wantErr := errors.New("apply failed")
	cleanupErr := errors.New("cleanup failed")
	factory := &fakeFactory{build: func(_ context.Context, node plan.NodeSpec, _ *r42concurrency.Scope) (golden.ApplyBlock, error) {
		return newFakeBlock(node.Address, func() error { return wantErr }, func(context.Context) error { return cleanupErr }), nil
	}}
	runner := executor.New(factory, nil)
	_, err := runner.Apply(t.Context(), savedPlan(t, []plan.NodeSpec{{Address: "research.bad", Kind: "research", Config: cty.EmptyObjectVal}}, nil), 1)

	var applyErr *executor.ApplyError
	require.ErrorAs(t, err, &applyErr)
	warnings := applyErr.CleanupWarnings()
	require.Len(t, warnings, 1)
	require.ErrorIs(t, warnings[0], cleanupErr)
	warnings[0] = nil
	require.Error(t, applyErr.CleanupWarnings()[0])
}

func savedPlan(t *testing.T, nodes []plan.NodeSpec, outputs map[string]plan.OutputSpec) *plan.Plan {
	t.Helper()
	planned, err := plan.New(t.TempDir(), nodes, outputs)
	require.NoError(t, err)
	return planned
}

type fakeFactory struct {
	build func(context.Context, plan.NodeSpec, *r42concurrency.Scope) (golden.ApplyBlock, error)
	err   error
}

func (f *fakeFactory) New(ctx context.Context, node plan.NodeSpec, scope *r42concurrency.Scope) (golden.ApplyBlock, error) {
	if f.err != nil {
		return nil, f.err
	}
	if f.build == nil {
		return nil, nil
	}
	return f.build(ctx, node, scope)
}

type fakeBlock struct {
	*golden.BaseBlock
	address string
	apply   func() error
	cleanup func(context.Context) error
}

var _ golden.ApplyBlock = (*fakeBlock)(nil)

func newFakeBlock(address string, apply func() error, cleanup func(context.Context) error) *fakeBlock {
	return &fakeBlock{BaseBlock: new(golden.BaseBlock), address: address, apply: apply, cleanup: cleanup}
}

func (*fakeBlock) Type() string            { return "" }
func (*fakeBlock) BlockType() string       { return "fixture" }
func (*fakeBlock) AddressLength() int      { return 2 }
func (*fakeBlock) CanExecutePrePlan() bool { return false }
func (b *fakeBlock) Address() string       { return b.address }
func (b *fakeBlock) Apply() error          { return b.apply() }
func (b *fakeBlock) Cleanup(ctx context.Context) error {
	if b.cleanup == nil {
		return nil
	}
	return b.cleanup(ctx)
}

type fakeCloser struct{ close func() error }

func (c *fakeCloser) Close() error { return c.close() }

var _ io.Closer = (*fakeCloser)(nil)

type eventLog struct {
	mu     sync.Mutex
	events []string
}

func (l *eventLog) add(event string) {
	l.mu.Lock()
	l.events = append(l.events, event)
	l.mu.Unlock()
}

func (l *eventLog) values() []string {
	l.mu.Lock()
	defer l.mu.Unlock()
	return append([]string(nil), l.events...)
}

func eventIndex(t *testing.T, events []string, want string) int {
	t.Helper()
	for index, event := range events {
		if event == want {
			return index
		}
	}
	require.Fail(t, fmt.Sprintf("event %q is missing from %v", want, events))
	return -1
}
