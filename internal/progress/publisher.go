package progress

import (
	"context"
	"errors"
	"sync"
	"time"

	"github.com/lonegunmanb/r42/internal/debuglog"
)

// Default publisher bounds. Queue sizes are internal, tested constants; they
// are deliberately not user-facing tuning flags.
const (
	defaultPublisherCapacity = 256
	defaultDrainTimeout      = 5 * time.Second
)

// Publisher subscribes to the synchronous event bus and writes JSONL progress
// records to the negotiated encoder behind a bounded asynchronous worker.
//
// Research execution must never wait indefinitely for the JSONL consumer, so
// Observe never blocks: when the bounded buffer is full, the publisher
// coalesces older pending node_upsert values per address, drops older
// timeline_append records first, and gives structural and terminal records
// priority over timeline records. Delivery remains best effort: missing,
// reordered, or dropped records never change research execution or its result.
type Publisher struct {
	encoder   *Encoder
	projector *Projector
	runID     string

	capacity     int
	drainTimeout time.Duration
	warn         func(string)

	mu         sync.Mutex
	priority   []Record // structural + terminal, drained first, never dropped
	upserts    map[string]Record
	timelines  []Record
	seq        uint64
	started    bool
	closed     bool
	finished   bool
	stopped    bool // true after the first write failure; publication disabled
	warned     bool
	stop       chan struct{}
	workerDone chan struct{}
	wake       chan struct{}
}

// PublisherOption customizes a Publisher. Tests use it to shrink the buffer
// and inject timeouts; production uses the internal defaults.
type PublisherOption func(*Publisher)

// WithPublisherCapacity overrides the bounded pending-item cap. It exists for
// deterministic saturation tests; production keeps the internal default.
func WithPublisherCapacity(capacity int) PublisherOption {
	return func(p *Publisher) { p.capacity = capacity }
}

// WithWarning sets the at-most-once warning sink for post-negotiation stdout
// failures.
func WithWarning(warn func(string)) PublisherOption {
	return func(p *Publisher) { p.warn = warn }
}

// WithDrainTimeout bounds the Close() flush attempt. It exists for tests;
// production keeps the internal default.
func WithDrainTimeout(timeout time.Duration) PublisherOption {
	return func(p *Publisher) { p.drainTimeout = timeout }
}

// NewPublisher returns a Publisher that projects events through projector and
// writes encoded records for runID through encoder. The publisher is inert
// until Start() is called.
func NewPublisher(encoder *Encoder, projector *Projector, runID string, options ...PublisherOption) *Publisher {
	publisher := &Publisher{
		encoder: encoder, projector: projector, runID: runID,
		capacity: defaultPublisherCapacity, drainTimeout: defaultDrainTimeout,
		warn:    func(string) {},
		upserts: make(map[string]Record),
		stop:    make(chan struct{}), workerDone: make(chan struct{}), wake: make(chan struct{}),
	}
	for _, apply := range options {
		if apply != nil {
			apply(publisher)
		}
	}
	return publisher
}

// Observe implements debuglog.EventObserver. It updates the projector and
// enqueues the derived records without blocking, so a stalled consumer never
// stalls research.
//
// Lock-order invariant: Observe acquires the projector lock (inside
// projector.Observe and recordsForEvent) and releases it fully before
// acquiring p.mu, so the two locks are never nested. Do not move record
// derivation inside the p.mu critical section.
func (p *Publisher) Observe(event debuglog.Event) {
	p.projector.Observe(event)
	records := p.recordsForEvent(event)
	if len(records) == 0 {
		return
	}
	p.mu.Lock()
	if p.closed {
		p.mu.Unlock()
		return
	}
	for _, record := range records {
		p.enqueue(record)
	}
	signalWake(p.wake)
	p.mu.Unlock()
}

// recordsForEvent derives the wire records for one event from projector state.
func (p *Publisher) recordsForEvent(event debuglog.Event) []Record {
	if event.Action == "dynamic.tasks.materialized" {
		if _, ok := p.projector.Node(event.BlockAddress); !ok {
			return nil
		}
		return []Record{p.projector.Materialized(event.BlockAddress)}
	}
	node, ok := p.projector.Node(event.BlockAddress)
	if !ok {
		return nil
	}
	records := []Record{&NodeRecord{Node: node}}
	switch event.Action {
	case "assistant.reasoning", "assistant.reasoning_delta",
		"assistant.message", "assistant.message_delta",
		"tool.execution_start", "tool.execution_progress",
		"tool.execution_partial_result", "tool.execution_complete":
		if timeline := p.projector.Timeline(event.BlockAddress); timeline != nil {
			records = append(records, timeline)
		}
	}
	return records
}

// enqueue places one record under the bounded pressure policy. Callers must
// hold p.mu.
func (p *Publisher) enqueue(record Record) {
	switch typed := record.(type) {
	case *NodeRecord:
		address := typed.Node.BlockAddress
		if _, exists := p.upserts[address]; exists {
			p.upserts[address] = record // coalesce: keep the newest pending state
			return
		}
		if p.overCapacity() && !p.evictOldestTimeline() {
			return // no droppable item; best effort drops the new record
		}
		p.upserts[address] = record
	case *TimelineRecord:
		if p.overCapacity() && !p.evictOldestTimeline() {
			return // queue full of upserts/priority; drop the new timeline
		}
		p.timelines = append(p.timelines, record)
	default:
		// Structural and terminal records are given priority over timeline
		// records: evict timelines to make room. The priority list itself is
		// bounded by the small structural/terminal record set, so in practice
		// room is always found; the eviction guard is a defensive fallback.
		for p.overCapacity() {
			if !p.evictOldestTimeline() {
				return
			}
		}
		p.priority = append(p.priority, record)
	}
}

func (p *Publisher) overCapacity() bool {
	return len(p.priority)+len(p.upserts)+len(p.timelines) >= p.capacity
}

// evictOldestTimeline drops the oldest pending timeline record, returning
// whether one was available.
func (p *Publisher) evictOldestTimeline() bool {
	if len(p.timelines) == 0 {
		return false
	}
	p.timelines = p.timelines[1:]
	return true
}

// Start emits the initial run_snapshot and spawns the writer worker. The
// snapshot is enqueued so Start never blocks on a stalled consumer.
func (p *Publisher) Start() {
	p.mu.Lock()
	if p.started {
		p.mu.Unlock()
		return
	}
	p.started = true
	p.enqueue(p.projector.SnapshotRecord())
	signalWake(p.wake)
	p.mu.Unlock()

	go p.run()
}

// Finish adds the one terminal record that describes the completed Apply.
// Like Observe, it is best effort: a disabled or closed publisher leaves
// research outcome untouched.
func (p *Publisher) Finish(applyErr error) {
	total := len(p.projector.Snapshot().Nodes)
	record := terminalRecord(total, applyErr)

	p.mu.Lock()
	if p.closed || p.finished {
		p.mu.Unlock()
		return
	}
	p.finished = true
	p.enqueue(record)
	signalWake(p.wake)
	p.mu.Unlock()
}

func terminalRecord(total int, applyErr error) Record {
	if applyErr == nil {
		return &RunCompletedRecord{
			Status: StatusSucceeded, Total: total, Succeeded: total,
		}
	}
	if errors.Is(applyErr, context.Canceled) {
		return &RunCanceledRecord{Status: StatusCanceled, Summary: "Apply canceled"}
	}
	return &RunFailedRecord{Status: StatusFailed, Summary: "Apply failed"}
}

// Close signals the worker to drain remaining pending records, waits up to the
// bounded drain timeout for one flush attempt, and then returns without
// blocking indefinitely.
func (p *Publisher) Close() {
	p.mu.Lock()
	if p.closed {
		p.mu.Unlock()
		return
	}
	p.closed = true
	close(p.stop)
	signalWake(p.wake)
	started := p.started
	p.mu.Unlock()
	if !started {
		return
	}

	select {
	case <-p.workerDone:
	case <-time.After(p.drainTimeout):
		// Bounded: abandon the flush attempt after the drain timeout.
	}
}

// run drains pending records until Close marks the publisher closed and the
// queue is empty.
func (p *Publisher) run() {
	defer close(p.workerDone)
	for {
		record := p.nextPending()
		if record == nil {
			p.mu.Lock()
			closed := p.closed
			empty := len(p.priority)+len(p.upserts)+len(p.timelines) == 0
			p.mu.Unlock()
			if closed && empty {
				return
			}
			select {
			case <-p.stop:
			case <-p.wake:
			case <-time.After(5 * time.Millisecond):
			}
			continue
		}
		p.emit(record)
	}
}

// nextPending returns the next record to write under the priority policy:
// structural/terminal first, then upserts, then timelines in FIFO order.
func (p *Publisher) nextPending() Record {
	p.mu.Lock()
	defer p.mu.Unlock()
	if len(p.priority) > 0 {
		record := p.priority[0]
		p.priority = p.priority[1:]
		return record
	}
	for address, record := range p.upserts {
		delete(p.upserts, address)
		return record
	}
	if len(p.timelines) > 0 {
		record := p.timelines[0]
		p.timelines = p.timelines[1:]
		return record
	}
	return nil
}

// signalWake unblocks a worker waiting on p.wake. The channel is never closed
// during normal operation, so a stale signal is harmless: the worker re-checks
// the queue after waking.
func signalWake(ch chan struct{}) {
	select {
	case ch <- struct{}{}:
	default:
	}
}

// emit encodes one record through the encoder. A write failure disables
// further progress publication: the publisher stops attempting to write,
// produces at most one warning, and never changes research execution.
func (p *Publisher) emit(record Record) {
	p.mu.Lock()
	stopped := p.stopped
	p.mu.Unlock()
	if stopped {
		return
	}
	p.seq++
	err := p.encoder.EncodeRecord(Envelope{RunID: p.runID, Sequence: p.seq}, record)
	if err == nil {
		return
	}
	p.mu.Lock()
	p.stopped = true
	if !p.warned {
		p.warned = true
		p.warn("JSONL progress publication stopped: " + err.Error())
	}
	p.mu.Unlock()
}
