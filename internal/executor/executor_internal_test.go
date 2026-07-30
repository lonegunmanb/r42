package executor

import (
	"context"
	"errors"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/Azure/golden"
	r42concurrency "github.com/lonegunmanb/r42/internal/concurrency"
	"github.com/lonegunmanb/r42/internal/plan"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/zclconf/go-cty/cty"
)

func TestScheduleDoesNotStartModuleAfterCancellation(t *testing.T) {
	t.Parallel()

	node := plan.NodeSpec{Address: "module.child", Kind: "module", Config: cty.EmptyObjectVal}
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	state := newRunState([]plan.NodeSpec{node}, func() {})
	scope, err := r42concurrency.NewScope(1)
	require.NoError(t, err)
	factory := &countingFactory{}
	events := make(chan nodeEvent, 2)

	state.schedule(factory, ctx, scope, node, events)
	running := state.running
	if running != 0 {
		ready := <-events
		close(ready.ticket.release)
		<-events
	}

	assert.Zero(t, running)
	assert.Zero(t, factory.calls.Load())
}

func TestRunNodeRechecksCancellationBeforeModuleFactory(t *testing.T) {
	t.Parallel()

	node := plan.NodeSpec{Address: "module.child", Kind: "module", Config: cty.EmptyObjectVal}
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	scope, err := r42concurrency.NewScope(1)
	require.NoError(t, err)
	factory := &countingFactory{}
	events := make(chan nodeEvent, 2)

	go runNode(factory, ctx, scope, node, func(error) {}, events)
	event := <-events
	if event.ticket != nil {
		close(event.ticket.release)
		<-events
	}

	assert.Nil(t, event.ticket)
	require.ErrorIs(t, event.err, context.Canceled)
	assert.Zero(t, factory.calls.Load())
}

func TestRunNodeStopsWhileWaitingForResearchPermit(t *testing.T) {
	t.Parallel()

	scope, err := r42concurrency.NewScope(1)
	require.NoError(t, err)
	releaseHolder := make(chan struct{})
	holderStarted := make(chan struct{})
	holderDone := make(chan error, 1)
	go func() {
		holderDone <- scope.WithResearch(t.Context(), func(context.Context) error {
			close(holderStarted)
			<-releaseHolder
			return nil
		})
	}()
	<-holderStarted

	ctx := newObservedContext()
	factory := &countingFactory{}
	events := make(chan nodeEvent, 1)
	node := plan.NodeSpec{Address: "research.waiting", Kind: "research", Config: cty.EmptyObjectVal}
	go runNode(factory, ctx, scope, node, func(error) {}, events)
	<-ctx.observed
	ctx.cancel()
	event := <-events

	assert.True(t, event.done)
	require.ErrorIs(t, event.err, context.Canceled)
	assert.Zero(t, factory.calls.Load())
	close(releaseHolder)
	require.NoError(t, <-holderDone)
}

type countingFactory struct{ calls atomic.Int64 }

func (f *countingFactory) New(
	context.Context,
	plan.NodeSpec,
	*r42concurrency.Scope,
) (golden.ApplyBlock, error) {
	f.calls.Add(1)
	return nil, errors.New("unexpected factory call")
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

func (c *observedContext) Deadline() (time.Time, bool) { return time.Time{}, false }

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
