package executor

import (
	"context"
	"fmt"
	"io"
	"sync"

	"github.com/Azure/golden"
	r42concurrency "github.com/lonegunmanb/r42/internal/concurrency"
	"github.com/lonegunmanb/r42/internal/plan"
	"github.com/zclconf/go-cty/cty"
)

type Factory interface {
	New(context.Context, plan.NodeSpec, *r42concurrency.Scope) (golden.ApplyBlock, error)
}

type OutputResolver interface {
	ResolveOutputs(*plan.Plan) (map[string]cty.Value, error)
}

type CleanupBlock interface {
	Cleanup(context.Context) error
}

type Executor struct {
	applyMu  sync.Mutex
	mu       sync.Mutex
	factory  Factory
	debug    io.Closer
	warnings []error
}

func New(factory Factory, debug io.Closer) *Executor {
	return &Executor{factory: factory, debug: debug}
}

func (e *Executor) Apply(
	ctx context.Context,
	planned *plan.Plan,
	parallelism int,
) (map[string]cty.Value, error) {
	scope, err := r42concurrency.NewScope(parallelism)
	if err != nil {
		return nil, err
	}
	return e.apply(ctx, planned, scope, true)
}

func (e *Executor) ApplyInScope(
	ctx context.Context,
	planned *plan.Plan,
	scope *r42concurrency.Scope,
) (map[string]cty.Value, error) {
	return e.apply(ctx, planned, scope, false)
}

func (e *Executor) apply(
	ctx context.Context,
	planned *plan.Plan,
	scope *r42concurrency.Scope,
	closeDebug bool,
) (map[string]cty.Value, error) {
	e.applyMu.Lock()
	defer e.applyMu.Unlock()
	e.setWarnings(nil)

	if e.factory == nil {
		return nil, fmt.Errorf("apply block factory is required")
	}
	if planned == nil {
		return nil, fmt.Errorf("saved plan is required")
	}
	if scope == nil {
		return nil, fmt.Errorf("concurrency scope is required")
	}

	runCtx, cancel := context.WithCancel(ctx)
	defer cancel()
	nodes := planned.Nodes()
	state := newRunState(nodes, cancel)
	if contextErr := ctx.Err(); contextErr != nil {
		state.fail(contextErr)
	}
	events := make(chan nodeEvent, max(1, len(nodes)*2))
	for _, node := range nodes {
		if state.remaining[node.Address] == 0 {
			state.schedule(e.factory, runCtx, scope, node, events)
		}
	}

	contextDone := runCtx.Done()
	for state.running > 0 {
		select {
		case event := <-events:
			state.handle(event, e.factory, runCtx, scope, events)
		case <-contextDone:
			state.fail(ctx.Err())
			contextDone = nil
		}
	}

	warnings := state.warnings
	outputs := plannedOutputs(planned)
	failure := state.failureError()
	if failure == nil {
		if resolver, ok := e.factory.(OutputResolver); ok {
			resolved, resolveErr := resolver.ResolveOutputs(planned)
			if resolveErr != nil {
				failure = fmt.Errorf("resolve apply outputs: %w", resolveErr)
			} else {
				outputs = resolved
			}
		}
	}
	if closeDebug && e.debug != nil {
		if closeErr := e.debug.Close(); closeErr != nil {
			warnings = append(warnings, fmt.Errorf("close debug log: %w", closeErr))
		}
	}
	e.setWarnings(warnings)
	if failure != nil {
		return nil, &ApplyError{cause: failure, cleanup: warnings}
	}
	return outputs, nil
}

func plannedOutputs(planned *plan.Plan) map[string]cty.Value {
	outputs := planned.Outputs()
	result := make(map[string]cty.Value, len(outputs))
	for name, output := range outputs {
		result[name] = output.Value
	}
	return result
}

func (e *Executor) Warnings() []error {
	e.mu.Lock()
	defer e.mu.Unlock()
	return append([]error(nil), e.warnings...)
}

func (e *Executor) setWarnings(warnings []error) {
	e.mu.Lock()
	e.warnings = append([]error(nil), warnings...)
	e.mu.Unlock()
}

type ApplyError struct {
	cause   error
	cleanup []error
}

func (e *ApplyError) Error() string { return e.cause.Error() }

func (e *ApplyError) Unwrap() error { return e.cause }

func (e *ApplyError) CleanupWarnings() []error {
	return append([]error(nil), e.cleanup...)
}

type nodePhase uint8

const (
	nodePending nodePhase = iota
	nodeApplying
	nodeParked
	nodeCleaning
	nodeDone
)

type cleanupTicket struct {
	address string
	block   golden.ApplyBlock
	release chan struct{}
}

type nodeEvent struct {
	address string
	ticket  *cleanupTicket
	err     error
	warning error
	done    bool
}

type runState struct {
	failureMu   sync.Mutex
	nodes       map[string]plan.NodeSpec
	downstreams map[string][]string
	remaining   map[string]int
	phases      map[string]nodePhase
	tickets     map[string]*cleanupTicket
	running     int
	failure     error
	warnings    []error
	cancel      context.CancelFunc
}

func newRunState(nodes []plan.NodeSpec, cancel context.CancelFunc) *runState {
	state := &runState{
		nodes:       make(map[string]plan.NodeSpec, len(nodes)),
		downstreams: make(map[string][]string, len(nodes)),
		remaining:   make(map[string]int, len(nodes)),
		phases:      make(map[string]nodePhase, len(nodes)),
		tickets:     make(map[string]*cleanupTicket),
		cancel:      cancel,
	}
	for _, node := range nodes {
		state.nodes[node.Address] = node
		state.remaining[node.Address] = len(node.Dependencies)
		state.phases[node.Address] = nodePending
		for _, dependency := range node.Dependencies {
			state.downstreams[dependency] = append(state.downstreams[dependency], node.Address)
		}
	}
	return state
}

func (s *runState) schedule(
	factory Factory,
	ctx context.Context,
	scope *r42concurrency.Scope,
	node plan.NodeSpec,
	events chan<- nodeEvent,
) {
	if s.hasFailed() {
		return
	}
	if err := ctx.Err(); err != nil {
		s.fail(err)
		return
	}
	s.phases[node.Address] = nodeApplying
	s.running++
	go runNode(factory, ctx, scope, node, s.fail, events)
}

func (s *runState) fail(err error) {
	if err == nil {
		return
	}
	s.failureMu.Lock()
	defer s.failureMu.Unlock()
	if s.failure != nil {
		return
	}
	s.failure = err
	s.cancel()
}

func (s *runState) hasFailed() bool {
	s.failureMu.Lock()
	defer s.failureMu.Unlock()
	return s.failure != nil
}

func (s *runState) failureError() error {
	s.failureMu.Lock()
	defer s.failureMu.Unlock()
	return s.failure
}

func (s *runState) handle(
	event nodeEvent,
	factory Factory,
	ctx context.Context,
	scope *r42concurrency.Scope,
	events chan<- nodeEvent,
) {
	if event.ticket != nil {
		s.phases[event.address] = nodeParked
		s.tickets[event.address] = event.ticket
		if !s.hasFailed() {
			s.release(event.address)
		}
	}
	if !event.done {
		if s.hasFailed() {
			s.releaseParkedWhenApplyStopped()
		}
		return
	}

	s.phases[event.address] = nodeDone
	delete(s.tickets, event.address)
	s.running--
	if event.warning != nil {
		s.warnings = append(s.warnings, event.warning)
	}
	if event.err != nil {
		s.fail(event.err)
	}
	if s.hasFailed() {
		s.releaseParkedWhenApplyStopped()
		return
	}
	for _, downstream := range s.downstreams[event.address] {
		s.remaining[downstream]--
		if s.remaining[downstream] == 0 {
			s.schedule(factory, ctx, scope, s.nodes[downstream], events)
		}
	}
}

func (s *runState) release(address string) {
	ticket := s.tickets[address]
	s.phases[address] = nodeCleaning
	close(ticket.release)
}

func (s *runState) releaseParkedWhenApplyStopped() {
	for address, phase := range s.phases {
		if phase == nodeApplying {
			return
		}
		if phase == nodeCleaning {
			return
		}
		_ = address
	}
	for address := range s.tickets {
		s.release(address)
	}
}

func runNode(
	factory Factory,
	ctx context.Context,
	scope *r42concurrency.Scope,
	node plan.NodeSpec,
	fail func(error),
	events chan<- nodeEvent,
) {
	if err := ctx.Err(); err != nil {
		err = fmt.Errorf("apply block %s: %w", node.Address, err)
		fail(err)
		events <- nodeEvent{address: node.Address, err: err, done: true}
		return
	}
	ticketSent := false
	run := func(context.Context) error {
		block, err := factory.New(ctx, node, scope)
		if err != nil {
			err = fmt.Errorf("create apply block %s: %w", node.Address, err)
		} else if block == nil {
			err = fmt.Errorf("create apply block %s: factory returned nil", node.Address)
		} else if applyErr := block.Apply(); applyErr != nil {
			err = fmt.Errorf("apply block %s: %w", node.Address, applyErr)
		}
		if err != nil {
			fail(err)
		}
		ticket := &cleanupTicket{address: node.Address, block: block, release: make(chan struct{})}
		ticketSent = true
		events <- nodeEvent{address: node.Address, ticket: ticket, err: err}
		<-ticket.release
		var warning error
		if cleanup, ok := block.(CleanupBlock); ok {
			if cleanupErr := cleanup.Cleanup(context.WithoutCancel(ctx)); cleanupErr != nil {
				warning = fmt.Errorf("cleanup block %s: %w", node.Address, cleanupErr)
			}
		}
		events <- nodeEvent{address: node.Address, err: err, warning: warning, done: true}
		return nil
	}

	var err error
	if node.Kind == "research" {
		err = scope.WithResearch(ctx, run)
	} else {
		err = run(ctx)
	}
	if ticketSent {
		return
	}
	err = fmt.Errorf("apply block %s: %w", node.Address, err)
	fail(err)
	events <- nodeEvent{address: node.Address, err: err, done: true}
}
