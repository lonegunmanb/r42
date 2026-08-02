package executor_test

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/Azure/golden"
	r42concurrency "github.com/lonegunmanb/r42/internal/concurrency"
	"github.com/lonegunmanb/r42/internal/debuglog"
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
		{Address: "research.static.source", Kind: "research", Config: cty.EmptyObjectVal},
		{Address: "research.static.report", Kind: "research", Dependencies: []string{"research.static.source"}, Config: cty.EmptyObjectVal},
	}, map[string]plan.OutputSpec{"status": {Value: cty.StringVal("complete")}})
	runner := executor.New(factory, nil)

	outputs, err := runner.Apply(t.Context(), planned, 2)

	require.NoError(t, err)
	assert.Equal(t, []string{"apply research.static.source", "apply research.static.report"}, events.values())
	assert.Equal(t, cty.StringVal("complete"), outputs["status"])
	assert.Empty(t, runner.Warnings())
}

func TestExecutorLifecycleEventsUseFactoryCanonicalAddress(t *testing.T) {
	t.Parallel()

	bus := debuglog.NewEventBus()
	var observed []debuglog.Event
	var observedMu sync.Mutex
	unsubscribe := bus.Subscribe(func(event debuglog.Event) {
		observedMu.Lock()
		observed = append(observed, event)
		observedMu.Unlock()
	})
	t.Cleanup(unsubscribe)
	ctx := debuglog.WithEventBus(t.Context(), bus)
	factory := &canonicalFactory{fakeFactory: fakeFactory{build: func(
		_ context.Context, node plan.NodeSpec, _ *r42concurrency.Scope,
	) (golden.ApplyBlock, error) {
		return newFakeBlock(node.Address, func() error { return nil }, nil), nil
	}}, prefix: "module.sources"}
	planned := savedPlan(t, []plan.NodeSpec{
		{Address: "research.static.collect", Kind: "research", Config: cty.EmptyObjectVal},
	}, nil)

	_, err := executor.New(factory, nil).Apply(ctx, planned, 1)
	require.NoError(t, err)
	observedMu.Lock()
	events := append([]debuglog.Event(nil), observed...)
	observedMu.Unlock()

	var addresses []string
	for _, event := range events {
		if event.Action == "block.apply" {
			addresses = append(addresses, event.BlockAddress)
		}
	}
	require.NotEmpty(t, addresses)
	assert.Equal(t, []string{
		"module.sources.research.static.collect",
		"module.sources.research.static.collect",
	}, addresses)
}

func TestExecutorRunsIndependentBlocksInParallelThroughGoldenRunPlan(t *testing.T) {
	t.Parallel()

	var active atomic.Int64
	var maximum atomic.Int64
	secondStarted := make(chan struct{})
	var closeSecond sync.Once
	factory := &fakeFactory{build: func(_ context.Context, node plan.NodeSpec, _ *r42concurrency.Scope) (golden.ApplyBlock, error) {
		return newFakeBlock(node.Address, func() error {
			current := active.Add(1)
			for observed := maximum.Load(); current > observed && !maximum.CompareAndSwap(observed, current); observed = maximum.Load() {
			}
			if current == 2 {
				closeSecond.Do(func() { close(secondStarted) })
			}
			select {
			case <-secondStarted:
			case <-time.After(50 * time.Millisecond):
			}
			active.Add(-1)
			return nil
		}, nil), nil
	}}
	planned := savedPlan(t, []plan.NodeSpec{
		{Address: "research.static.first", Kind: "research", Config: cty.EmptyObjectVal},
		{Address: "research.static.second", Kind: "research", Config: cty.EmptyObjectVal},
	}, nil)

	_, err := executor.New(factory, nil).Apply(t.Context(), planned, 2)

	require.NoError(t, err)
	assert.Equal(t, int64(2), maximum.Load())
}

func TestResearchPlanImplementsGoldenPlan(t *testing.T) {
	t.Parallel()

	assert.Implements(t, (*golden.Plan)(nil), new(executor.ResearchPlan))
}

func TestResearchConfigImplementsGoldenConfigAndParallelism(t *testing.T) {
	t.Parallel()

	assert.Implements(t, (*golden.Config)(nil), new(executor.ResearchConfig))
	assert.Implements(t, (*golden.Parallelism)(nil), new(executor.ResearchConfig))
}

func TestExecutorResolvesOutputsAfterSuccessfulApply(t *testing.T) {
	t.Parallel()

	factory := &resolvingFactory{
		fakeFactory: fakeFactory{build: func(_ context.Context, node plan.NodeSpec, _ *r42concurrency.Scope) (golden.ApplyBlock, error) {
			return newFakeBlock(node.Address, func() error { return nil }, nil), nil
		}},
		outputs: map[string]cty.Value{"result": cty.StringVal("applied")},
	}
	planned := savedPlan(t, []plan.NodeSpec{{
		Address: "research.static.source", Kind: "research", Config: cty.EmptyObjectVal,
	}}, map[string]plan.OutputSpec{"result": {Value: cty.UnknownVal(cty.String)}})

	outputs, err := executor.New(factory, nil).Apply(t.Context(), planned, 1)

	require.NoError(t, err)
	assert.Equal(t, cty.StringVal("applied"), outputs["result"])
	assert.True(t, factory.resolved)
}

func TestExecutorApplyInScopeUsesCallerScopeWithoutClosingDebug(t *testing.T) {
	t.Parallel()

	scope, err := r42concurrency.NewScope(4)
	require.NoError(t, err)
	var received *r42concurrency.Scope
	factory := &fakeFactory{build: func(_ context.Context, node plan.NodeSpec, actual *r42concurrency.Scope) (golden.ApplyBlock, error) {
		received = actual
		return newFakeBlock(node.Address, func() error { return nil }, nil), nil
	}}
	debug := &fakeCloser{}
	planned := savedPlan(t, []plan.NodeSpec{{
		Address: "research.static.source", Kind: "research", Config: cty.EmptyObjectVal,
	}}, nil)
	runner := executor.New(factory, debug)

	_, err = runner.ApplyInScope(t.Context(), planned, scope)

	require.NoError(t, err)
	assert.Same(t, scope, received)
	assert.Zero(t, debug.calls.Load())
}

func TestExecutorPreservesPrimaryFailureAndCleanupWarnings(t *testing.T) {
	t.Parallel()

	wantErr := errors.New("primary apply failure")
	closeErr := errors.New("close session failed")
	debugErr := errors.New("flush debug failed")
	events := &eventLog{}
	factory := &fakeFactory{build: func(_ context.Context, node plan.NodeSpec, _ *r42concurrency.Scope) (golden.ApplyBlock, error) {
		switch node.Address {
		case "research.static.failure":
			return newFakeBlock(node.Address, func() error {
				events.add("apply failed")
				return wantErr
			}, func(context.Context) error {
				events.add("close failure session")
				return closeErr
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
		{Address: "research.static.failure", Kind: "research", Config: cty.EmptyObjectVal},
		{Address: "research.static.dependent", Kind: "research", Dependencies: []string{"research.static.failure"}, Config: cty.EmptyObjectVal},
	}, nil)
	runner := executor.New(factory, debug)

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
	assert.Less(t, eventIndex(t, actual, "apply failed"), eventIndex(t, actual, "close failure session"))
	assert.Equal(t, len(actual)-1, eventIndex(t, actual, "flush debug"))
}

func TestExecutorRecordsSkippedOutputResolutionAfterBlockFailure(t *testing.T) {
	t.Parallel()

	logDirectory := t.TempDir()
	recorder, err := debuglog.NewRecorder(logDirectory, true)
	require.NoError(t, err)
	ctx := debuglog.WithRecorder(t.Context(), recorder)
	factory := &fakeFactory{build: func(_ context.Context, node plan.NodeSpec, _ *r42concurrency.Scope) (golden.ApplyBlock, error) {
		return newFakeBlock(node.Address, func() error { return assert.AnError }, nil), nil
	}}
	planned := savedPlan(t, []plan.NodeSpec{{Address: "research.static.failure", Kind: "research", Config: cty.EmptyObjectVal}}, nil)

	_, err = executor.New(factory, nil).Apply(ctx, planned, 1)
	require.Error(t, err)
	require.NoError(t, recorder.Close())

	content, err := os.ReadFile(filepath.Join(logDirectory, debuglog.EventsFileName))
	require.NoError(t, err)
	assert.Contains(t, string(content), `"action":"apply.outputs.resolve","status":"skipped"`)
	assert.NotContains(t, string(content), `"action":"apply.outputs.resolve","status":"failed"`)
	assert.NotContains(t, string(content), `"block_address":"research.static.failure","block_type":"research","action":"block.decode","status":"started"`)
	assert.Equal(t, 1, strings.Count(string(content), `"block_address":"research.static.failure","block_type":"research","action":"block.decode","status":"completed"`))
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
	planned := savedPlan(t, []plan.NodeSpec{{Address: "research.static.failure", Kind: "research", Config: cty.EmptyObjectVal}}, nil)

	_, err := runner.Apply(t.Context(), planned, 1)

	require.ErrorIs(t, err, wantErr)
	assert.True(t, closed)
	for _, warning := range runner.Warnings() {
		require.NotErrorIs(t, warning, cleanupContextErr)
	}
}

func TestExecutorCleansNestedApplyBeforeDebugFlush(t *testing.T) {
	t.Parallel()

	events := &eventLog{}
	nestedStopped := make(chan struct{})
	factory := &fakeFactory{build: func(_ context.Context, node plan.NodeSpec, _ *r42concurrency.Scope) (golden.ApplyBlock, error) {
		if node.Address == "module.child" {
			return newFakeBlock(node.Address, func() error {
				events.add("nested applied")
				return nil
			}, func(context.Context) error {
				events.add("nested stopped")
				close(nestedStopped)
				return nil
			}), nil
		}
		return newFakeBlock(node.Address, func() error {
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
		{Address: "research.static.failure", Kind: "research", Dependencies: []string{"module.child"}, Config: cty.EmptyObjectVal},
	}, nil)
	runner := executor.New(factory, debug)

	_, err := runner.Apply(t.Context(), planned, 2)

	require.Error(t, err)
	assert.Equal(t, []string{"nested applied", "nested stopped", "debug closed"}, events.values())
	assert.Empty(t, runner.Warnings())
}

func TestExecutorDoesNotResumeCompletedNodes(t *testing.T) {
	t.Parallel()

	var applied int
	factory := &fakeFactory{build: func(_ context.Context, node plan.NodeSpec, _ *r42concurrency.Scope) (golden.ApplyBlock, error) {
		return newFakeBlock(node.Address, func() error { applied++; return nil }, nil), nil
	}}
	planned := savedPlan(t, []plan.NodeSpec{{Address: "research.static.once", Kind: "research", Config: cty.EmptyObjectVal}}, nil)
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
		{Address: "research.static.first", Kind: "research", Config: cty.EmptyObjectVal},
		{Address: "research.static.second", Kind: "research", Config: cty.EmptyObjectVal},
	}, nil)
	runner := executor.New(factory, nil)

	_, err := runner.Apply(t.Context(), planned, 1)

	require.NoError(t, err)
	assert.Equal(t, int64(1), maximum.Load())
	assert.Zero(t, active.Load())
}

func TestExecutorCancelsResearchWaitingForPermitWithoutCreatingBlock(t *testing.T) {
	t.Parallel()

	scope, err := r42concurrency.NewScope(1)
	require.NoError(t, err)
	holderStarted := make(chan struct{})
	releaseHolder := make(chan struct{})
	holderDone := make(chan error, 1)
	go func() {
		holderDone <- scope.WithResearch(t.Context(), func(context.Context) error {
			close(holderStarted)
			<-releaseHolder
			return nil
		})
	}()
	<-holderStarted

	var factoryCalls atomic.Int64
	factory := &fakeFactory{build: func(_ context.Context, node plan.NodeSpec, _ *r42concurrency.Scope) (golden.ApplyBlock, error) {
		factoryCalls.Add(1)
		return newFakeBlock(node.Address, func() error { return nil }, nil), nil
	}}
	planned := savedPlan(t, []plan.NodeSpec{{Address: "research.static.waiting", Kind: "research", Config: cty.EmptyObjectVal}}, nil)
	runner := executor.New(factory, nil)
	ctx := newObservedContext()
	result := make(chan error, 1)
	go func() {
		_, applyErr := runner.ApplyInScope(ctx, planned, scope)
		result <- applyErr
	}()
	<-ctx.observed
	ctx.cancel()

	applyErr := <-result

	require.ErrorIs(t, applyErr, context.Canceled)
	assert.Zero(t, factoryCalls.Load())
	close(releaseHolder)
	require.NoError(t, <-holderDone)
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

func TestExecutorReturnsCancellationWhenApplyDoesNotObserveContext(t *testing.T) {
	t.Parallel()

	ctx, cancel := context.WithCancel(t.Context())
	started := make(chan struct{})
	release := make(chan struct{})
	factory := &fakeFactory{build: func(context.Context, plan.NodeSpec, *r42concurrency.Scope) (golden.ApplyBlock, error) {
		return newFakeBlock("module.child", func() error {
			close(started)
			<-release
			return nil
		}, nil), nil
	}}
	planned := savedPlan(t, []plan.NodeSpec{{
		Address: "module.child",
		Kind:    "module",
		Config:  cty.EmptyObjectVal,
		Module:  &plan.ModuleSpec{Plan: savedPlan(t, nil, nil)},
	}}, nil)
	finished := make(chan error, 1)
	go func() {
		_, err := executor.New(factory, nil).Apply(ctx, planned, 1)
		finished <- err
	}()
	<-started
	cancel()
	close(release)

	require.ErrorIs(t, <-finished, context.Canceled)
}

func TestExecutorReturnsCancellationWhenContextIsCanceledDuringCleanup(t *testing.T) {
	t.Parallel()

	ctx, cancel := context.WithCancel(t.Context())
	cleanupStarted := make(chan struct{})
	releaseCleanup := make(chan struct{})
	factory := &fakeFactory{build: func(context.Context, plan.NodeSpec, *r42concurrency.Scope) (golden.ApplyBlock, error) {
		return newFakeBlock("research.static.subject", func() error { return nil }, func(context.Context) error {
			close(cleanupStarted)
			<-releaseCleanup
			return nil
		}), nil
	}}
	planned := savedPlan(t, []plan.NodeSpec{{
		Address: "research.static.subject",
		Kind:    "research",
		Config:  cty.EmptyObjectVal,
	}}, nil)
	finished := make(chan error, 1)
	go func() {
		_, err := executor.New(factory, nil).Apply(ctx, planned, 1)
		finished <- err
	}()
	<-cleanupStarted
	cancel()
	close(releaseCleanup)

	require.ErrorIs(t, <-finished, context.Canceled)
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
		{name: "factory error", runner: executor.New(&fakeFactory{err: factoryErr}, nil), planned: savedPlan(t, []plan.NodeSpec{{Address: "research.static.bad", Kind: "research", Config: cty.EmptyObjectVal}}, nil), want: "create apply block research.static.bad: factory failed"},
		{name: "nil block", runner: executor.New(&fakeFactory{}, nil), planned: savedPlan(t, []plan.NodeSpec{{Address: "research.static.bad", Kind: "research", Config: cty.EmptyObjectVal}}, nil), want: "create apply block research.static.bad: factory returned nil"},
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
	_, err := runner.Apply(t.Context(), savedPlan(t, []plan.NodeSpec{{Address: "research.static.bad", Kind: "research", Config: cty.EmptyObjectVal}}, nil), 1)

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
	planned, err := plan.NewWithContextAndLocals(t.TempDir(), nodes, outputs, nil, nil)
	require.NoError(t, err)
	return planned
}

type fakeFactory struct {
	build func(context.Context, plan.NodeSpec, *r42concurrency.Scope) (golden.ApplyBlock, error)
	err   error
}

type canonicalFactory struct {
	fakeFactory
	prefix string
}

func (f *canonicalFactory) CanonicalAddress(address string) string {
	return f.prefix + "." + address
}

type resolvingFactory struct {
	fakeFactory
	outputs  map[string]cty.Value
	resolved bool
}

func (f *resolvingFactory) ResolveOutputs(*plan.Plan) (map[string]cty.Value, error) {
	f.resolved = true
	return f.outputs, nil
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

type fakeCloser struct {
	close func() error
	calls atomic.Int32
}

func (c *fakeCloser) Close() error {
	c.calls.Add(1)
	if c.close == nil {
		return nil
	}
	return c.close()
}

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

type observedContext struct {
	done     chan struct{}
	observed chan struct{}
	once     sync.Once
	canceled atomic.Bool
}

func newObservedContext() *observedContext {
	return &observedContext{done: make(chan struct{}), observed: make(chan struct{})}
}

func (*observedContext) Deadline() (time.Time, bool) { return time.Time{}, false }

func (c *observedContext) Done() <-chan struct{} {
	c.once.Do(func() { close(c.observed) })
	return c.done
}

func (c *observedContext) Err() error {
	if c.canceled.Load() {
		return context.Canceled
	}
	return nil
}

func (*observedContext) Value(any) any { return nil }

func (c *observedContext) cancel() {
	c.canceled.Store(true)
	close(c.done)
}
