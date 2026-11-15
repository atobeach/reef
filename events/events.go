// Package events dispatches application events: Dispatch runs the event's
// listeners and, when it is broadcastable, publishes it. Tests call Fake to
// record events instead, and assert on them with eventstest.
//
//	events.Listen(d, func(ctx context.Context, e OrderShipped) error { ... })
//	err := d.Dispatch(ctx, OrderShipped{...})
package events

import (
	"context"
	"reflect"
	"sync"

	"github.com/atobeach/reef/broadcasting"
)

// Dispatcher runs listeners and broadcasts.
type Dispatcher struct {
	mu          sync.RWMutex
	listeners   map[reflect.Type][]func(ctx context.Context, event any) error
	broadcaster broadcasting.Broadcaster
	fake        *fake
}

// NewDispatcher builds a dispatcher publishing broadcastable events through
// broadcaster (nil drops them).
func NewDispatcher(broadcaster broadcasting.Broadcaster) *Dispatcher {
	if broadcaster == nil {
		broadcaster = broadcasting.Null{}
	}

	return &Dispatcher{listeners: map[reflect.Type][]func(context.Context, any) error{}, broadcaster: broadcaster}
}

// Listen registers a listener for events of type T.
func Listen[T any](d *Dispatcher, listener func(ctx context.Context, event T) error) {
	d.mu.Lock()
	defer d.mu.Unlock()

	key := reflect.TypeFor[T]()
	d.listeners[key] = append(d.listeners[key], func(ctx context.Context, event any) error {
		return listener(ctx, event.(T))
	})
}

// Dispatch runs event's listeners in order, then publishes a ShouldBroadcast
// event inline. The first error stops the chain, broadcast included.
func (d *Dispatcher) Dispatch(ctx context.Context, event any) error {
	key := reflect.TypeOf(event)

	d.mu.RLock()
	listeners := append([]func(context.Context, any) error{}, d.listeners[key]...)
	faked := d.fake
	d.mu.RUnlock()

	if faked != nil && faked.covers(key) {
		faked.record(event)

		return nil
	}

	for _, listener := range listeners {
		if err := listener(ctx, event); err != nil {
			return err
		}
	}

	if broadcastable, ok := event.(broadcasting.ShouldBroadcast); ok {
		return broadcasting.Broadcast(ctx, d.broadcaster, broadcastable)
	}

	return nil
}

// fake records the covered types instead of dispatching them; an empty set
// covers everything.
type fake struct {
	mu         sync.Mutex
	types      map[reflect.Type]bool
	dispatched []any
}

func (f *fake) covers(key reflect.Type) bool {
	return len(f.types) == 0 || f.types[key]
}

func (f *fake) record(event any) {
	f.mu.Lock()
	defer f.mu.Unlock()

	f.dispatched = append(f.dispatched, event)
}

// Fake records events of the samples' types (no samples: every type) from
// now on, instead of running their listeners and broadcasts.
func (d *Dispatcher) Fake(samples ...any) {
	f := &fake{types: map[reflect.Type]bool{}}
	for _, sample := range samples {
		f.types[reflect.TypeOf(sample)] = true
	}

	d.mu.Lock()
	d.fake = f
	d.mu.Unlock()
}

// Dispatched is every recorded event of type T, in order.
func Dispatched[T any](d *Dispatcher) []T {
	d.mu.RLock()
	f := d.fake
	d.mu.RUnlock()

	if f == nil {
		return nil
	}

	f.mu.Lock()
	defer f.mu.Unlock()

	var events []T
	for _, event := range f.dispatched {
		if typed, ok := event.(T); ok {
			events = append(events, typed)
		}
	}

	return events
}
