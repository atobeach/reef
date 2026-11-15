# Events

- [Introduction](#introduction)
- [Defining Events](#defining-events)
- [Defining Listeners](#defining-listeners)
- [Registering Events and Listeners](#registering-events-and-listeners)
- [Dispatching Events](#dispatching-events)
    - [Listener Errors](#listener-errors)
- [Broadcasting Events](#broadcasting-events)
- [Queued Event Listeners](#queued-event-listeners)
- [Testing](#testing)
    - [Faking a Subset of Events](#faking-a-subset-of-events)
- [Available Methods](#available-methods)

## Introduction

reef's events (`github.com/atobeach/reef/events`) provide a simple observer pattern implementation, allowing you to subscribe and listen for various events that occur within your application. Events serve as a great way to decouple various aspects of your application, since a single event can have multiple listeners that do not depend on each other. For example, when a user signs up, your application may record when they last signed in and send them a welcome email, and the code that creates the account knows about neither.

The heart of the package is the `*events.Dispatcher`. A typical reef application builds one at boot and hands it around as `a.Events`:

```go
dispatcher := events.NewDispatcher(broadcaster)
```

`NewDispatcher` takes the [broadcaster](broadcasting.md) that publishes broadcastable events; `nil` drops them.

## Defining Events

An event is a plain Go struct that carries the data its listeners need. There is nothing to implement, and no base type to embed. Events usually live in an `app/events` package:

```go
package events

import "example.com/flights/app/models"

// Registered fires when a new account exists.
type Registered struct {
	User models.User
}
```

reef dispatches some events of its own too, such as `auth.Login[models.User]` when a user signs in; see [authentication](authentication.md).

## Defining Listeners

A listener is a function that receives a context and the event, and returns an error:

```go
func(ctx context.Context, event appevents.Registered) error
```

That may be a closure, a plain function or a method value. A listener with dependencies is a struct whose fields hold them, and its methods are the listeners:

```go
// AuthAnalytics captures the account lifecycle (`signed_up`,
// `logged_in`) in the analytics service.
type AuthAnalytics struct {
	Funnel analytics.Funnel
}

// CaptureRegistration handles Registered.
func (p AuthAnalytics) CaptureRegistration(ctx context.Context, e appevents.Registered) error {
	p.capture(ctx, "signed_up", e.User)

	return nil
}

// CaptureLogin handles Login.
func (p AuthAnalytics) CaptureLogin(ctx context.Context, e auth.Login[models.User]) error {
	p.capture(ctx, "logged_in", e.User)

	return nil
}
```

The context is the one the event was dispatched on, so a listener reads the request's facts (the signed-in user, the database connection and its transaction) from it, as any other code below the controllers does.

## Registering Events and Listeners

`events.Listen` registers a listener for one event type, which Go infers from the listener's parameter. A typical application registers them all at boot, from one function its bootstrap calls:

```go
// RegisterEventListeners wires the auth listeners: on login the user's
// last sign-in is recorded and on registration a welcome email is sent,
// then the analytics service records the event.
func RegisterEventListeners(a *app.App, s *Services) {
	analytics := listeners.AuthAnalytics{Funnel: s.Funnel}

	events.Listen(a.Events, func(ctx context.Context, e auth.Login[models.User]) error {
		return listeners.RecordLastLogin(ctx, e.User.ID)
	})
	events.Listen(a.Events, analytics.CaptureLogin)

	events.Listen(a.Events, func(ctx context.Context, e appevents.Registered) error {
		return listeners.SendWelcomeMail(ctx, a.Mail, e.User)
	})
	events.Listen(a.Events, analytics.CaptureRegistration)
}
```

reef has no event discovery and no subscriber classes: a provider function like this one, registering several listeners, plays the part of a subscriber.

> [!WARNING]
> Listeners are matched by the event's exact dynamic type. A listener for `Registered` never sees a `*Registered`, and a listener whose parameter is an interface type never runs: always listen for, and dispatch, the same concrete type.

## Dispatching Events

To dispatch an event, call `Dispatch` on the dispatcher with the context its listeners should run on and the event. For example, a registration controller that holds the dispatcher in its `Events` field does so once the account exists:

```go
user, err := ctl.CreateNewUser.Create(c, account.NewUser{Name: request.Name, Email: request.Email, Password: request.Password})
if err != nil {
	return nil, err
}

if err := ctl.Events.Dispatch(c, appevents.Registered{User: user}); err != nil {
	return nil, err
}
```

`Dispatch` runs the event's listeners synchronously, one after another, in the order they were registered, and returns once they have all run. An event without listeners, and that isn't broadcastable, does nothing.

### Listener Errors

The first listener that returns an error stops the chain: the remaining listeners are skipped, the event isn't broadcast, and `Dispatch` returns that error. A listener that shouldn't be able to fail its event, such as an analytics call, should log or report its own errors and return `nil`.

## Broadcasting Events

An event that implements `broadcasting.ShouldBroadcast` (`BroadcastOn`, `BroadcastAs` and `BroadcastWith`) is also published to its channels, after its listeners have run, through the broadcaster the dispatcher was built with. Publishing is synchronous, and its error is `Dispatch`'s. For example, a `flights:check-delays` command might dispatch a broadcastable `FlightDelayed` for each flight whose departure has slipped:

```go
for _, flight := range flights {
	// ...

	// An unreachable broker must not abort the batch or fail every run of
	// the outage: the flight stays unannounced and retries next minute.
	if err := cmd.Events.Dispatch(c, appevents.FlightDelayed{Flight: flight, Minutes: delay}); err != nil {
		c.Report(err)

		continue
	}

	// ...
}
```

See [broadcasting](broadcasting.md) for defining broadcastable events and for the realtime server.

## Queued Event Listeners

reef doesn't queue listeners. If a listener's work is slow, or talks to a third party that may be down, have the listener dispatch a [job](queues.md) and return: the job then gets its own retries, timeout and failure handling.

## Testing

When testing code that dispatches events, you may wish to instruct reef to not actually execute the event's listeners, since the listeners' code can be tested directly and separately of the code that dispatches the event. Call `Fake` on the dispatcher: from then on, events are recorded instead of dispatched, their listeners and broadcasts skipped. `events.Dispatched[T]` returns the recorded events of one type, in order, and the `eventstest` package asserts on them. The test below boots the app through a [test case](testing.md#a-full-test-case) of your own (`tests.Setup`):

```go
func TestAnnouncesADelayedFlightOnce(t *testing.T) {
	tc := tests.Setup(t)
	tc.FreezeTime()
	tc.App.Events.Fake(appevents.FlightDelayed{})

	departs := clock.Now(tc.Ctx).Add(2 * time.Hour)
	delayed := factories.Flight(tc).Create(func(f *models.Flight) {
		f.DepartsAt, f.EstimatedDepartsAt = departs, new(departs.Add(30*time.Minute))
	})

	tc.RunCommand("flights:check-delays").AssertSuccessful()

	eventstest.AssertDispatchedTimes[appevents.FlightDelayed](t, tc.App.Events, 1)
	eventstest.AssertDispatched(t, tc.App.Events, func(e appevents.FlightDelayed) bool {
		return e.Flight.ID == delayed.ID && e.Minutes == 30
	})
}
```

See [mocking](mocking.md) for every `eventstest` assertion.

### Faking a Subset of Events

The values you pass to `Fake` are samples: only events of their types are recorded, and every other event is dispatched as usual, listeners and all. In the example above, only `FlightDelayed` is faked. Calling `Fake` with no arguments records every event:

```go
tc.App.Events.Fake()
```

Each call to `Fake` starts a fresh recording, replacing the previous one. Before `Fake` is called, `events.Dispatched` returns `nil`.

## Available Methods

- `events.NewDispatcher(broadcaster)`: a dispatcher publishing broadcastable events through `broadcaster`; `nil` drops them.
- `events.Listen[T](dispatcher, listener)`: registers a listener for events of type `T`.
- `Dispatcher.Dispatch(ctx, event)`: runs the event's listeners in registration order, then broadcasts it when it is a `ShouldBroadcast`; the first error stops the chain and is returned.
- `Dispatcher.Fake(samples...)`: records events of the samples' types (every type, with no samples) instead of dispatching them.
- `events.Dispatched[T](dispatcher)`: the recorded events of type `T`, in order; `Dispatched[any]` returns all of them.
