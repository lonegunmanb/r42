package debuglog

import (
	"context"
	"sync"
	"time"
)

type EventObserver func(Event)

type EventBus struct {
	mu         sync.RWMutex
	dispatchMu sync.Mutex
	nextID     uint64
	sequence   uint64
	observers  map[uint64]EventObserver
}

type eventBusContextKey struct{}

func NewEventBus() *EventBus {
	return &EventBus{observers: make(map[uint64]EventObserver)}
}

func WithEventBus(ctx context.Context, bus *EventBus) context.Context {
	return context.WithValue(ctx, eventBusContextKey{}, bus)
}

func EventBusFromContext(ctx context.Context) *EventBus {
	if ctx == nil {
		return nil
	}
	bus, _ := ctx.Value(eventBusContextKey{}).(*EventBus)
	return bus
}

func (b *EventBus) Subscribe(observer EventObserver) func() {
	if b == nil || observer == nil {
		return func() {}
	}
	b.mu.Lock()
	b.nextID++
	id := b.nextID
	b.observers[id] = observer
	b.mu.Unlock()
	return func() {
		b.mu.Lock()
		delete(b.observers, id)
		b.mu.Unlock()
	}
}

func (b *EventBus) publish(event Event) {
	if b == nil {
		return
	}
	_ = b.dispatch(event, nil)
}

func (b *EventBus) dispatch(event Event, beforeNotify func(Event) error) error {
	if b == nil {
		if beforeNotify == nil {
			return nil
		}
		return beforeNotify(event)
	}
	b.dispatchMu.Lock()
	defer b.dispatchMu.Unlock()
	event = b.prepare(event)
	if beforeNotify != nil {
		if err := beforeNotify(event); err != nil {
			return err
		}
	}
	b.notify(event)
	return nil
}

func (b *EventBus) prepare(event Event) Event {
	if b == nil {
		return event
	}
	b.mu.Lock()
	b.sequence++
	event.Sequence = b.sequence
	if event.Timestamp.IsZero() {
		event.Timestamp = time.Now().UTC()
	}
	b.mu.Unlock()
	return event
}

func (b *EventBus) notify(event Event) {
	if b == nil {
		return
	}
	b.mu.RLock()
	observers := make([]EventObserver, 0, len(b.observers))
	for _, observer := range b.observers {
		observers = append(observers, observer)
	}
	b.mu.RUnlock()
	for _, observer := range observers {
		observer(event)
	}
}
