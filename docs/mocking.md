# Mocking

- [Introduction](#introduction)
- [Mocking Objects](#mocking-objects)
- [Mail Fake](#mail-fake)
    - [Queued Mail](#queued-mail)
- [Queue Fake](#queue-fake)
- [Event Fake](#event-fake)
- [Broadcast Fake](#broadcast-fake)
- [HTTP Client Mock](#http-client-mock)
    - [Faking Responses](#faking-responses)
    - [Inspecting Requests](#inspecting-requests)
    - [Unmocked Requests](#unmocked-requests)
- [AI Provider Fake](#ai-provider-fake)
    - [Scripting Streamed Steps](#scripting-streamed-steps)
    - [Scripting Completions](#scripting-completions)
    - [Inspecting Prompts](#inspecting-prompts)
- [Interacting With Time](#interacting-with-time)
    - [Faking Sleep](#faking-sleep)
    - [Goroutines and Timers](#goroutines-and-timers)
- [Available Assertions](#available-assertions)
    - [mailtest](#mailtest)
    - [queuetest](#queuetest)
    - [eventstest](#eventstest)
    - [broadcastingtest](#broadcastingtest)
    - [httpclienttest](#httpclienttest)
    - [clocktest](#clocktest)
    - [aitest](#aitest)

## Introduction

When testing a reef application, you will want to keep some of its effects from happening for real: an email from going out, a job from running, a request from reaching a third-party API, a test from waiting out a real minute. reef gives every such service an in-memory fake that records what the app did, and an `xxxtest` package of assertions over what it recorded:

| Service | Fake | Assertions |
|---|---|---|
| [Mail](mail.md) | `mail.Fake` (a transport) | `reef/mail/mailtest` |
| [Queues](queues.md) | `queue.Fake` (a dispatcher) | `reef/queue/queuetest` |
| [Events](events.md) | `(*events.Dispatcher).Fake` | `reef/events/eventstest` |
| [Broadcasting](broadcasting.md) | `broadcasting.Fake` (a broadcaster) | `reef/broadcasting/broadcastingtest` |
| [HTTP Client](http-client.md) | `httpclient.MockClient` | `reef/httpclient/httpclienttest` |
| [AI](ai.md) | scripted replies on the `MockClient` | `reef/ai/aitest` |
| Time ([helpers](helpers.md)) | a `clock.Clock` of the test's own | `reef/clock/clocktest` |

reef has no facades and no service container to swap a binding in. A fake is a value: your app is built with its services handed over explicitly, and a test builds it with the fakes instead. A test case, for example, might boot the app on these connections:

```go
tc.connections = bootstrap.Connections{
	DB:          conn,
	Cache:       cache.NewMemoryStore(),
	RateLimits:  ratelimit.NewMemoryStore(),
	Mail:        tc.Mail,       // &mail.Fake{}
	Broadcaster: tc.Broadcasts, // &broadcasting.Fake{}
	HTTP:        tc.HTTP,       // httpclient.NewMock(t, replies)
	Clock:       now,           // &clock.Clock{}
}
```

Every assertion fails the test at once (`t.Fatalf`), and its message lists what the fake did record, so a miss shows you what happened instead. Each assertion takes the `testing.TB` first, then the fake; the readers that hand back what was recorded (`mailtest.Sent`, `httpclienttest.Sent`) take just the fake.

## Mocking Objects

For your own dependencies, reef relies on plain Go rather than a mocking library. Give a controller, command or action its dependencies as exported fields typed by what it needs, small interfaces where it helps, and build it in the test with a fake of your own:

```go
// ExchangeRates is what the converter needs from the rates provider.
type ExchangeRates interface {
	Rate(ctx context.Context, currency string) (float64, bool)
}

// fixedRates answers every currency with one rate.
type fixedRates float64

func (r fixedRates) Rate(context.Context, string) (float64, bool) { return float64(r), true }

// In the test:
converter := Converter{Rates: fixedRates(1.17)}
```

A dependency that calls an outside API is usually better left real and answered by the [HTTP client mock](#http-client-mock): the test then covers your request building and response decoding too. If you have written a fake of your own and want assertions over it in reef's style, see [Writing Custom Assertions](testing.md#writing-custom-assertions).

> [!NOTE]
> The examples on this page use `tests.Setup` and `tc`, your application's own test case rather than part of reef. See [Building a Test Case](testing.md#building-a-test-case) for how to write one.

## Mail Fake

`mail.Fake` is a mail transport that records every message instead of delivering it. Build your mailer with it (`mail.MailerConfig{Transport: fake, ...}`) and the mailables your app sends are rendered as usual, templates and all, then kept by the fake with their recipients, subject, HTML and the mailable value itself.

```go
func TestSendsAResetPasswordEmailWhenRequested(t *testing.T) {
	tc := tests.Setup(t)
	user := factories.User(tc).Create()

	tc.Browser().PostJSON("/forgot-password", map[string]any{"email": user.Email}).AssertOk()

	mailtest.AssertSentTimes[appmail.ResetPasswordMail](t, tc.Mail, 1)
	mailtest.AssertSentTo[appmail.ResetPasswordMail](t, tc.Mail, user.Email)
}
```

Mailables are matched by their Go type, given as the type parameter. `AssertSent` takes a function that receives both the mailable and the rendered `mail.Message`, so you may check the data the mailable carried or what it rendered:

```go
mailtest.AssertSent(t, tc.Mail, func(_ appmail.VerifyEmailMail, m mail.Message) bool {
	return m.HasTo(user.Email) && strings.Contains(m.HTML, code)
})
```

To inspect a message in detail, `mailtest.Sent[T]` returns every message rendered from a `T`, in order, and `fake.Sent()` every message of any type:

```go
html := mailtest.Sent[appmail.ResetPasswordMail](tc.Mail)[0].HTML
```

You may also assert on the total, or on nothing having gone out:

```go
mailtest.AssertSentCount(t, tc.Mail, 1)
mailtest.AssertNothingSent(t, tc.Mail)
```

Recipient checks, in `AssertSentTo` and `Message.HasTo`, ignore case.

### Queued Mail

A mailable that implements `mail.ShouldQueue`, sent through a mailer that has a queue, is pushed to the queue as a `mail.SendQueuedMailable` job instead of reaching the transport. On a [sync queue](#queue-fake) the job runs at once, so the message still lands in the fake by the time `Send` returns. On a `queue.Fake` it waits as a recorded job, which you may assert on, and run yourself:

```go
pushed := tc.FakeQueue()

tc.Browser().PostJSON("/register", registration).AssertCreated()

queuetest.AssertPushedTimes[mail.SendQueuedMailable](t, pushed, 1)

// Run the queued mail as the worker would.
if err := tc.App.Mail.HandleQueued(tc.Ctx, queuetest.Pushed[mail.SendQueuedMailable](pushed)[0]); err != nil {
	t.Fatal(err)
}
mailtest.AssertSentTo[appmail.WelcomeMail](t, tc.Mail, "ana@example.com")
```

The job names its mailable by `MailKind` in `Mailable`. On a mailer with an encrypter, the mailable's fields and recipients travel only inside `Sealed`, so a test that looks inside a queued mail opens it first with the mailer's `Open`, which hands back the job with `Payload` and `To` decrypted (a job queued unsealed comes back as it is):

```go
job := queuetest.Pushed[mail.SendQueuedMailable](pushed)[0]

opened, err := tc.App.Mail.Open(job)
if err != nil {
	t.Fatal(err)
}

if opened.To[0] != user.Email {
	t.Fatalf("queued to %v", opened.To)
}
```

## Queue Fake

Two queue connections are useful in tests. `queue.Sync` runs each job inline, on the dispatcher's context and so inside the test's transaction, and returns its error to the dispatcher. A test environment usually runs on it by default (`QUEUE_CONNECTION=sync`), so a job's effects are there as soon as the code that dispatched it returns.

When a test is about what gets dispatched rather than what the job does, use `queue.Fake`, a dispatcher that records each job without running it. Hand it to your app wherever a `queue.Dispatcher` goes. A test case helper such as `tc.FakeQueue()` can reboot the app on one and return it; call it before the code under test runs:

```go
func TestQueuesAStatusRefreshForEachFlightDepartingToday(t *testing.T) {
	tc := tests.Setup(t)
	pushed := tc.FakeQueue()

	today := factories.Flight(tc).DepartingIn(3 * time.Hour).Create()
	factories.Flight(tc).DepartingIn(72 * time.Hour).Create()

	tc.RunCommand("flights:refresh-status").AssertSuccessful()

	queuetest.AssertPushedTimes[jobs.RefreshFlightStatus](t, pushed, 1)
	queuetest.AssertPushed(t, pushed, func(job jobs.RefreshFlightStatus) bool {
		return job.FlightID == today.ID
	})
}
```

Jobs are matched by their Go type. To read them, `queuetest.Pushed[T](fake)` returns the recorded jobs of type `T` and `fake.All()` every recorded job, both in dispatch order. You may also assert on the total, or on nothing having been pushed:

```go
queuetest.AssertCount(t, pushed, 1)
queuetest.AssertNothingPushed(t, pushed)
```

## Event Fake

Events are faked on the dispatcher itself. `Fake` takes a sample value of each event type to record; from then on those events are recorded instead of dispatched, so their listeners don't run and nothing is broadcast. Events of other types dispatch as usual. With no samples, every event is recorded.

```go
func TestWarnsATravellerTenMinutesBeforeTheirHoldEnds(t *testing.T) {
	tc := tests.Setup(t)
	tc.FreezeTime()
	tc.App.Events.Fake(appevents.HoldExpiring{})

	booking := factories.Booking(tc).Create(func(b *models.Booking) {
		b.HoldExpiresAt = clock.Now(tc.Ctx).Add(10 * time.Minute)
	})

	tc.RunCommand("bookings:send-hold-warnings").AssertSuccessful()

	eventstest.AssertDispatchedTimes[appevents.HoldExpiring](t, tc.App.Events, 1)
	eventstest.AssertDispatched(t, tc.App.Events, func(e appevents.HoldExpiring) bool {
		return e.Booking.ID == booking.ID && e.MinutesRemaining == 10
	})
}
```

Generic events are faked by their instantiated type, such as `auth.Login[models.User]{}`. To read recorded events, `events.Dispatched[T](d)` returns those of type `T`, in order.

> [!NOTE]
> Event types match exactly: a sample `OrderShipped{}` records `OrderShipped` values, not `*OrderShipped` pointers. Fake with the same kind of value your code dispatches. Calling `Fake` again starts a new recording, with only the new samples' types.

The dispatcher works the same way outside the app:

```go
d := events.NewDispatcher(nil)
d.Fake(OrderShipped{})

if err := d.Dispatch(t.Context(), OrderShipped{ID: 7}); err != nil {
	t.Fatal(err)
}

eventstest.AssertDispatched(t, d, func(e OrderShipped) bool { return e.ID == 7 })
```

## Broadcast Fake

`broadcasting.Fake` is a broadcaster that records each broadcast instead of publishing it: its channels, its event name and its payload, encoded exactly as it would go over the wire. Give it to your app as the broadcaster (here, `tc.Broadcasts`):

```go
tc.RunCommand("bookings:send-hold-warnings").AssertSuccessful()

broadcastingtest.AssertBroadcastCount(t, tc.Broadcasts, 1)
broadcastingtest.AssertBroadcastOn(t, tc.Broadcasts, "booking."+booking.Reference, "booking.hold-expiring")
```

`AssertBroadcastTimes` counts one event across every channel, and `AssertNothingBroadcast` asserts that nothing went out. `fake.Sent()` returns every recorded `broadcasting.Sent` (`Channels`, `Event`, `Payload` as `json.RawMessage`), in order, for a closer look at a payload:

```go
var payload appevents.HoldExpiringPayload
if err := json.Unmarshal(tc.Broadcasts.Sent()[0].Payload, &payload); err != nil {
	t.Fatal(err)
}

if payload.MinutesRemaining != 5 {
	t.Fatalf("minutes_remaining = %d, want 5", payload.MinutesRemaining)
}
```

To test how your app copes with the realtime server being down, set the fake's `Fail` function. It is called for every broadcast with its channels and event; an error it returns is what the broadcaster returns, and that broadcast is not recorded:

```go
tc.Broadcasts.Fail = func(channels []string, event string) error {
	return errors.New("broadcast connection refused")
}
```

> [!NOTE]
> An event recorded by the [event fake](#event-fake) is never broadcast. When a test asserts on broadcasts, leave the event's type unfaked.

## HTTP Client Mock

`httpclient.MockClient` answers your [HTTP client](http-client.md)'s requests in-process. `WithMock` returns a copy of a client that answers from the mock instead of the network:

```go
mock := httpclient.NewMock(t, map[string]any{
	"GetFlightStatus": httpclient.JSON(200, map[string]any{"status": "delayed", "delay_minutes": 25}),
})

client := httpclient.New(httpclient.Connector{BaseURL: "https://api.airline.example/v1"}).WithMock(mock)
```

Only the transport is replaced. The request is still built from your typed request, and the connector's headers, hooks, failure policy and retries all apply to the mocked replies. If your app builds every connector's client in one place, such as an `App.HTTP` method, have it call `WithMock(tc.HTTP)` in tests; a `tests.WithHTTP(replies)` option then only has to pass `replies` to `NewMock`.

### Faking Responses

Replies are keyed by the request's type name, as `httpclient.Name` spells it: the bare type name, with no package path, a pointer counted as the type it points to. A reply is one of three things.

A `httpclient.MockResponse` answers every request of that type the same way. `httpclient.JSON(status, body)` builds one from a Go value or a `json.RawMessage`, with `Content-Type: application/json`; build the struct yourself for any other body:

```go
httpclient.JSON(200, map[string]any{"flights": []any{}})

httpclient.MockResponse{Status: 200, Body: []byte("<svg/>"), Header: http.Header{"Content-Type": {"image/svg+xml"}}}
```

A `MockResponse` with `Err` set simulates a transport failure (a timeout, a refused connection) instead of a reply:

```go
"ListFlights": httpclient.MockResponse{Err: errors.New("connection refused")},
```

A `httpclient.Sequence` answers with each response in turn and then keeps repeating the last, which suits polling that progresses:

```go
"PollSearchResults": httpclient.Sequence{
	pollPage([]int{1}, false),
	pollPage([]int{2}, false),
	pollPage(nil, true),
},
```

A `httpclient.Responder` is a function that answers from what was sent, for a request type whose reply depends on its parameters:

```go
"ListAirports": httpclient.Responder(func(sent httpclient.Sent) httpclient.MockResponse {
	if strings.Contains(sent.HTTP.URL.RawQuery, "country=GB") {
		return airportsReply(britishAirports)
	}

	return airportsReply(nil)
}),
```

`mock.Add(name, reply)` sets or replaces the reply for a request type after the mock is built, for a test that changes what an API says mid-way; a `Sequence` added this way starts from its first response. `NewMock` and `Add` panic on a reply of any other type.

> [!NOTE]
> Retries run against the mock like any other reply, so a connector that retries a 503 three times consumes three replies of a `Sequence`. Their delay is a real wait, not the [test's clock](#faking-sleep): keep retry delays short in the test's config.

### Inspecting Requests

The mock records every request it receives, mocked or not. `httpclienttest` asserts on them by request type, given as a type parameter:

```go
httpclienttest.AssertSentTimes[airline.CreateBooking](t, tc.HTTP, 1)
httpclienttest.AssertNotSent[payments.CapturePayment](t, tc.HTTP)
httpclienttest.AssertSentCount(t, tc.HTTP, 3)
httpclienttest.AssertNothingSent(t, tc.HTTP)
```

`AssertSent` takes a function over each `httpclient.Sent`, which holds the typed `Request`, the built `*http.Request` (`HTTP`: method, URL, headers) and the raw `Body`:

```go
httpclienttest.AssertSent(t, tc.HTTP, func(s httpclient.Sent) bool {
	return s.HTTP.Method == http.MethodPatch && s.HTTP.URL.Path == "/v1/bookings/QX7P2M"
})
```

To read what went out, `httpclienttest.Sent[R](mock)` returns every request of type `R` in order (a value and a pointer to one count as the same type), and `mock.Sent()` every request of any type:

```go
sent := httpclienttest.Sent[airline.CreateBooking](tc.HTTP)

var body map[string]any
if err := json.Unmarshal(sent[0].Body, &body); err != nil {
	t.Fatal(err)
}
```

### Unmocked Requests

A request with no reply never reaches the network. The mock fails the test (with `Errorf`, naming the method, URL and request type) and the client's `Send` returns the error `httpclient: unmocked request <Name>`, so the code under test sees a failed call. A mock with no replies at all therefore fails any test that calls out.

`NewMock` takes an `httpclient.TestingT`, just the `Helper` and `Errorf` methods of `testing.TB`, so that `httpclient`, which your production code imports, never links the `testing` package into your binary. Any `*testing.T` satisfies it.

> [!WARNING]
> Because replies are keyed by bare type name, two request types with the same name in different packages share a reply. Give each request type a name of its own.

## AI Provider Fake

reef's [AI](ai.md) gateway talks to an OpenAI-compatible Chat Completions API through the HTTP client, so a test fakes the model by scripting the exact bodies a provider would send on the `MockClient`. A run then exercises the real gateway, agent loop and stream protocol without a live model call. The helpers live in `reef/ai/aitest`.

### Scripting Streamed Steps

An agent turn is a series of steps: the model calls a tool, gets its result, then answers. `aitest.Script` answers the streamed requests (`StreamChatCompletion`) with one reply per step, in order, the last repeating:

```go
aitest.Script(tc.HTTP,
	aitest.Stream(aitest.ToolCall("call-1", "SearchFlights", `{"origin":"LHR","destination":"BCN","date":"2026-11-05"}`)),
	aitest.Stream(aitest.Text("Here are the flights I found.")),
)

tc.ActingAs(user).PostJSON("/api/chat", chatPayload(conversationID, userMessage("Find me a flight to Barcelona"))).AssertOk()
```

`aitest.Stream(chunks)` is a 200 reply with `Content-Type: text/event-stream` whose body is the chunks as server-sent events. The chunks come from these builders, each a whole step that ends the way a provider ends one:

- `aitest.Text(deltas...)` streams the text split into the given deltas, then finishes with `stop` and a usage chunk.
- `aitest.ToolCall(id, name, arguments)` calls one tool, its JSON arguments streamed in two halves as providers do, then finishes with `tool_calls` and a usage chunk.
- `aitest.ToolCalls(calls...)` makes several tool calls side by side, each an `aitest.Call{ID, Name, Arguments}`.
- `aitest.ErrorChunk(code, message)` is a provider error reported inside the stream.

```go
aitest.Stream(aitest.ToolCalls(
	aitest.Call{ID: "call-1", Name: "AirportInfo", Arguments: `{"code":"HND"}`},
	aitest.Call{ID: "call-2", Name: "SearchFlights", Arguments: `{"origin":"LHR","destination":"HND"}`},
))

aitest.Script(mock, aitest.Stream(aitest.ErrorChunk(502, "Provider is overloaded")))
```

A chunk is an `aitest.Chunk`, a `map[string]any` encoded as one JSON event; `aitest.SSE(chunks)` renders a slice of them as the event-stream body, ending with `data: [DONE]`. Every chunk the step builders make, an error chunk aside, reports the model `aitest.Model` (`openai/gpt-test`), and so does `aitest.Completion`.

`aitest.Status(status, message)` is a failed provider reply, a JSON error body with that status, for testing failover and error handling:

```go
aitest.Script(mock, aitest.Status(402, "Insufficient credits"))
```

`Script` replaces whatever answered `StreamChatCompletion` before, and starts its steps from the first.

### Scripting Completions

Non-streamed calls (`ChatCompletion`), such as structured output, are scripted with `aitest.ScriptCompletions`, one reply per call in order, the last repeating. `aitest.Completion(text)` is a 200 reply whose message content is `text`; for structured output, that is the JSON object:

```go
aitest.ScriptCompletions(tc.HTTP,
	aitest.Completion(`{"summary":"Two direct flights a day.","highlights":["Morning departures"]}`),
)

aitest.ScriptCompletions(tc.HTTP, aitest.Status(503, "overloaded"))
```

### Inspecting Prompts

`aitest.SentBodies(mock)` decodes the request body of every chat completion sent, streamed or not, in order, and `aitest.SentRawBodies(mock)` returns them as sent, so you can check what the model was told:

```go
bodies := aitest.SentBodies(tc.HTTP)
if len(bodies) == 0 {
	t.Fatal("no model request was sent")
}

messages := bodies[len(bodies)-1]["messages"].([]any)
```

They also prove that nothing was sent, as when a turn is refused before the agent runs:

```go
if n := len(aitest.SentBodies(tc.HTTP)); n != 0 {
	t.Fatalf("prompted %d times", n)
}
```

## Interacting With Time

reef reads the time from the clock bound to the context, with `clock.Now(ctx)`, rather than from `time.Now` (see [Helpers](helpers.md)). A context without a clock reads the wall clock, in UTC. A test binds a `*clock.Clock` of its own, so moving one test's time never moves another's, and every query, factory and request in the test reads it:

- `TravelTo(t)` freezes the clock at `t` (in UTC).
- `Travel(d)` moves the clock by `d`, freezing it first if it isn't frozen yet. Pass a negative duration to travel into the past.
- `Now()` reads it.

There is no "travel back": once frozen, a clock stays frozen until the test moves it again, and the next test has a clock of its own.

`clocktest.FrozenAt(ctx, instant)` binds a context a new clock frozen at `instant`, and returns both, which is all a unit test needs:

```go
func TestMemoryStoreWindowsRunOnTheCallersClock(t *testing.T) {
	store := ratelimit.NewMemoryStore()
	ctx, now := clocktest.FrozenAt(t.Context(), time.Date(2026, 10, 20, 12, 0, 0, 0, time.UTC))

	if _, err := store.Hit(ctx, "bucket", time.Minute); err != nil {
		t.Fatal(err)
	}

	now.Travel(time.Minute)

	if hits, _, _ := store.Attempts(ctx, "bucket"); hits != 0 {
		t.Fatalf("the window ended but holds %d hits", hits)
	}
}
```

A test case can bind its clock for you and expose it as methods: `tc.FreezeTime()` freezes it at the current second (the precision of a `timestamp(0)` column) and returns that time, `tc.TravelTo(at)` and `tc.Travel(d)` move it:

```go
func TestFlightStatusWaitsFiveMinutesAfterAFailureBeforeAskingTheAirlineAgain(t *testing.T) {
	unavailable := httpclient.JSON(503, map[string]string{"error": "Unavailable"})
	tc := tests.Setup(t, tests.WithHTTP(map[string]any{
		// Three failures: the request itself retries twice before giving up.
		"GetFlightStatus": httpclient.Sequence{
			unavailable, unavailable, unavailable,
			httpclient.JSON(200, map[string]any{"status": "on_time"}),
		},
	}), tests.WithConfig(func(cfg *config.Config) { cfg.Services.Airline.APIKey = "test-key" }))
	unknown := map[string]any{"status": "unknown"}
	onTime := map[string]any{"status": "on_time"}

	tc.Get("/api/flights/FL123/status").AssertJSON(unknown)
	tc.Travel(4 * time.Minute)
	tc.Get("/api/flights/FL123/status").AssertJSON(unknown)
	httpclienttest.AssertSentTimes[airline.GetFlightStatus](t, tc.HTTP, 3)

	tc.Travel(2 * time.Minute)
	tc.Get("/api/flights/FL123/status").AssertJSON(onTime)
}
```

### Faking Sleep

Code that paces itself (polling an API, backing off a rate limit) pauses with `clock.Sleep(ctx, d)`. On a clock whose sleep is faked, `Sleep` returns at once and records the pause instead, so the test runs instantly and can assert on the pauses. Fake it with the test clock's `FakeSleep` method (`(*clock.Clock).FakeSleep`, or a test case helper such as `tc.FakeSleep()` that returns the clock), or bind a new one with `clocktest.FakeSleep(ctx)`. `clocktest.AssertSlept` then asserts the exact pauses, in order:

```go
func TestPollSearchResultsPausesTheConfiguredIntervalBetweenPolls(t *testing.T) {
	tc := airlineCase(t, map[string]any{"PollSearchResults": httpclient.Sequence{
		pollPage([]int{1}, false), pollPage([]int{2}, false), pollPage(nil, true),
	}})
	sleeps := tc.FakeSleep()

	if _, err := pollAction(tc, func(cfg *config.Airline) {
		cfg.MinResults = 100
		cfg.PollIntervalMs = 800
	}).Handle(tc.Ctx, "search-1", nil); err != nil {
		t.Fatal(err)
	}

	// Three polls, a pause between each, none after the last.
	clocktest.AssertSlept(t, sleeps, 800*time.Millisecond, 800*time.Millisecond)
}
```

With no durations, `AssertSlept` asserts that the code never paused. The clock's `Slept()` method lists the recorded pauses if you'd rather check them yourself. A faked sleep doesn't move the clock: travel it explicitly when the code compares times after a pause.

```go
ctx, sleeps := clocktest.FakeSleep(t.Context())

importer.Import(ctx, []airports.Row{
	{Code: "LHR", Name: "London Heathrow"},
	{Code: "XXX", Name: "Unknown"},
})

// Every lookup is paced for the supplier's rate limit, a failed one too.
clocktest.AssertSlept(t, sleeps, 100*time.Millisecond, 100*time.Millisecond)
```

### Goroutines and Timers

The context's clock moves `clock.Now` and `clock.Sleep` only. A context deadline, `time.After`, a ticker and a goroutine all run on real time. When every goroutine, timer and connection a unit test starts lives in memory, run the test in a [`testing/synctest`](https://pkg.go.dev/testing/synctest) bubble with `synctest.Test`.

Time in a bubble is virtual. It starts at 2000-01-01 UTC and jumps only when every goroutine in the bubble is blocked, so deadlines and retries pass instantly and in order, and a test can assert an exact duration. `synctest.Wait()` returns once every other goroutine in the bubble is blocked, which is how a test waits for a background goroutine rather than sleeping and polling:

```go
func TestFlexibleServesStaleWhileOneCallerRefreshes(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		// … five stale reads while compute is held on release …
		close(release)
		// The refresh has landed once nothing else in the bubble can run.
		synctest.Wait()

		if value, _ := cache.Flexible(ctx, store, "k", time.Hour, 4*time.Hour, compute); value != 2 {
			t.Fatalf("after the refresh = %d, want 2", value)
		}
	})
}
```

A bubble has limits:

- Use the `t` the bubble hands you (its `t.Context()` belongs to the bubble), and never call `t.Run` or `t.Parallel` inside it.
- A real socket, Valkey or Postgres never counts as blocked: while a goroutine waits on one, `synctest.Wait` doesn't return and time doesn't move. Connect through `net.Pipe` instead: reef/realtime's drain tests serve `realtime.Serve` on one.
- A test whose goroutines are all blocked for good panics with "deadlock: all goroutines in bubble are blocked", so a bug that would hang fails at once.
- Feature tests keep the context's clock: `tests.Setup` runs each test in parallel, and the database's `now()` doesn't follow bubble time.

## Available Assertions

### mailtest

- `mailtest.AssertSent[T](t, fake, func(mailable T, m mail.Message) bool)`: a `T` went out that the function accepts.
- `mailtest.AssertSentTo[T](t, fake, address)`: a `T` went out to `address` (case-insensitive).
- `mailtest.AssertSentTimes[T](t, fake, n)`: exactly `n` `T` mailables went out.
- `mailtest.AssertSentCount(t, fake, n)`: exactly `n` mails went out, of any mailable.
- `mailtest.AssertNothingSent(t, fake)`: no mail went out.
- `mailtest.Sent[T](fake) []mail.Message`: every message rendered from a `T`, in order.

### queuetest

- `queuetest.AssertPushed[T](t, fake, func(job T) bool)`: a recorded `T` satisfies the function.
- `queuetest.AssertPushedTimes[T](t, fake, n)`: exactly `n` jobs of type `T` were recorded.
- `queuetest.AssertCount(t, fake, n)`: exactly `n` jobs were recorded, of any type.
- `queuetest.AssertNothingPushed(t, fake)`: no job was recorded.
- `queuetest.Pushed[T](fake) []T`: the recorded jobs of type `T`, in dispatch order.

### eventstest

- `eventstest.AssertDispatched[T](t, dispatcher, func(event T) bool)`: a recorded `T` satisfies the function.
- `eventstest.AssertDispatchedTimes[T](t, dispatcher, n)`: exactly `n` events of type `T` were recorded.

### broadcastingtest

- `broadcastingtest.AssertBroadcastOn(t, fake, channel, event)`: `event` went out on `channel` (among its channels).
- `broadcastingtest.AssertBroadcastTimes(t, fake, event, n)`: `event` went out exactly `n` times.
- `broadcastingtest.AssertBroadcastCount(t, fake, n)`: exactly `n` broadcasts went out, of any event.
- `broadcastingtest.AssertNothingBroadcast(t, fake)`: nothing went out.

### httpclienttest

- `httpclienttest.AssertSent(t, mock, func(s httpclient.Sent) bool)`: some request satisfies the function.
- `httpclienttest.AssertSentTimes[R](t, mock, n)`: exactly `n` requests of type `R` were sent.
- `httpclienttest.AssertNotSent[R](t, mock)`: no request of type `R` was sent.
- `httpclienttest.AssertSentCount(t, mock, n)`: exactly `n` requests were sent, of any type.
- `httpclienttest.AssertNothingSent(t, mock)`: no request was sent.
- `httpclienttest.Sent[R](mock) []httpclient.Sent`: every request of type `R`, in order.

### clocktest

- `clocktest.FrozenAt(ctx, instant) (context.Context, *clock.Clock)`: binds `ctx` a new clock frozen at `instant`.
- `clocktest.FakeSleep(ctx) (context.Context, *clock.Clock)`: binds `ctx` a new clock whose `Sleep` records its pauses.
- `clocktest.AssertSlept(t, clock, want...)`: the faked `Sleep` paused for exactly `want`, in order; with no `want`, never.

### aitest

- `aitest.Script(mock, steps...)`: answers streamed chat completions with the steps in order, the last repeating.
- `aitest.ScriptCompletions(mock, steps...)`: the same for non-streamed chat completions.
- `aitest.Stream(chunks) httpclient.MockResponse`: a 200 event-stream reply for one streamed step.
- `aitest.Completion(text) httpclient.MockResponse`: a 200 JSON reply for one non-streamed step.
- `aitest.Status(status, message) httpclient.MockResponse`: a failed provider reply.
- `aitest.Text(deltas...) []aitest.Chunk`: a step streaming text, then `stop`.
- `aitest.ToolCall(id, name, arguments) []aitest.Chunk`: a step calling one tool, then `tool_calls`.
- `aitest.ToolCalls(calls...) []aitest.Chunk`: a step calling several tools; each an `aitest.Call{ID, Name, Arguments}`.
- `aitest.ErrorChunk(code, message) []aitest.Chunk`: a provider error inside the stream.
- `aitest.SSE(chunks) []byte`: the chunks as a server-sent-event body, ending `data: [DONE]`.
- `aitest.SentBodies(mock) []map[string]any`: every chat completion request body sent, decoded.
- `aitest.SentRawBodies(mock) []string`: every chat completion request body sent, raw.
- `aitest.Chunk`: one stream chunk, a `map[string]any`.
- `aitest.Model`: the model id scripted chunks report, `openai/gpt-test`.
