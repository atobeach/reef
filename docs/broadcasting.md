# Broadcasting

- [Introduction](#introduction)
- [Server Side Installation](#server-side-installation)
    - [Configuration](#configuration)
    - [Broadcast Drivers](#broadcast-drivers)
- [Client Side Installation](#client-side-installation)
- [Concept Overview](#concept-overview)
- [Defining Broadcast Events](#defining-broadcast-events)
    - [Broadcast Name](#broadcast-name)
    - [Broadcast Data](#broadcast-data)
    - [Broadcast Queue](#broadcast-queue)
    - [Broadcast Conditions](#broadcast-conditions)
- [Authorizing Channels](#authorizing-channels)
    - [Registering the Authorization Route](#registering-the-authorization-route)
    - [Defining Authorization Callbacks](#defining-authorization-callbacks)
- [Broadcasting Events](#broadcasting-events)
    - [Customizing the Connection](#customizing-the-connection)
    - [Anonymous Events](#anonymous-events)
    - [Rescuing Broadcasts](#rescuing-broadcasts)
- [Receiving Broadcasts](#receiving-broadcasts)
- [Client Events](#client-events)
- [The Realtime Server](#the-realtime-server)
    - [Running the Server](#running-the-server)
    - [Server Configuration](#server-configuration)
    - [Embedding the Server](#embedding-the-server)
    - [Scaling Across Instances](#scaling-across-instances)
    - [Connection Lifecycle and Error Codes](#connection-lifecycle-and-error-codes)
    - [The HTTP API](#the-http-api)
- [Testing](#testing)
- [Pusher Protocol Helpers](#pusher-protocol-helpers)

## Introduction

In many modern web applications, WebSockets are used to implement realtime, live-updating user interfaces. When some data is updated on the server, a message is sent over a WebSocket connection to be handled by the client, which is far cheaper than having the browser poll your API for changes.

reef lets you "broadcast" your [events](events.md) over a WebSocket connection. An event that implements `broadcasting.ShouldBroadcast` is published by the events dispatcher, through a `broadcasting.Broadcaster`, to reef's own WebSocket server in the `realtime` package. That server speaks the Pusher Channels protocol byte for byte, so the browser receives broadcasts with an unmodified [Laravel Echo](https://github.com/laravel/echo) and `pusher-js` client.

For example, a flight's departure can slip at any time. A flight-booking application might run a `flights:check-delays` command every minute; when a flight is delayed, the command dispatches a `FlightDelayed` event, and every browser looking at that flight shows the new departure time without ever polling for it.

There are two packages involved:

- `github.com/atobeach/reef/broadcasting` is the publishing side: the `ShouldBroadcast` interface, the broadcast drivers, and the `/broadcasting/auth` route that signs private-channel subscriptions.
- `github.com/atobeach/reef/realtime` is the WebSocket server itself, the equivalent of Laravel Reverb. You run it as its own process with the `reverb:start` command.

## Server Side Installation

### Configuration

reef reads no environment variables of its own: your application's configuration builds a `broadcasting.PusherConfig` for the publisher and a `realtime.Config` for the server (see [configuration](configuration.md)). These examples follow Laravel's variable names:

```ini
BROADCAST_CONNECTION=reverb

REVERB_APP_ID=flights
REVERB_APP_KEY=app-key
REVERB_APP_SECRET=app-secret

# Where the publisher reaches the realtime server's HTTP API...
REVERB_HOST=127.0.0.1
REVERB_PORT=8080
REVERB_SCHEME=http
```

The publisher's settings live on a `PusherConfig`. Set every field: the publisher POSTs to `{Scheme}://{Host}:{Port}/apps/{AppID}/events` and signs each request with `Key` and `Secret`:

```go
import "github.com/atobeach/reef/broadcasting"

pusher := broadcasting.NewPusher(broadcasting.PusherConfig{
	AppID:  "flights",
	Key:    "app-key",
	Secret: "app-secret",
	Host:   "127.0.0.1",
	Port:   8080,
	Scheme: "http",
})
```

`NewPusher` gives every request a 10 second timeout. The publisher is usually called from a scheduled command, and a dead realtime server should never stall the minute's run.

### Broadcast Drivers

A `Broadcaster` is anything with a `Broadcast(ctx, channels, event, payload) error` method. reef ships four:

| Driver | Type | What it does |
| --- | --- | --- |
| Pusher | `*broadcasting.Pusher` | Publishes through the Pusher HTTP API: reef's realtime server, or Pusher itself. |
| Log | `broadcasting.Log{}` | Writes each broadcast to the log at info level: `Broadcasting [event] on channels [a, b] with payload:` with a `payload` attribute. |
| Null | `broadcasting.Null{}` | Drops every broadcast. |
| Fake | `*broadcasting.Fake` | Records broadcasts for [assertions](#testing). |

Picking a driver is your application's job. For example, an application may pick one from `BROADCAST_CONNECTION` (default `null`) when it boots:

```go
// pusher is the reverb or pusher connection's Pusher, nil for any other
// BROADCAST_CONNECTION.
func pusher(cfg config.Broadcasting) *broadcasting.Pusher {
	if cfg.Connection == "reverb" || cfg.Connection == "pusher" {
		return broadcasting.NewPusher(cfg.Pusher())
	}

	return nil
}

// broadcaster is BROADCAST_CONNECTION's driver.
func broadcaster(cfg config.Broadcasting, pusher *broadcasting.Pusher) broadcasting.Broadcaster {
	switch {
	case pusher != nil:
		return pusher
	case cfg.Connection == "log":
		return broadcasting.Log{}
	default:
		return broadcasting.Null{}
	}
}
```

The same `*broadcasting.Pusher` also signs private-channel subscriptions, so keep a handle on it for the [authorization route](#registering-the-authorization-route).

## Client Side Installation

Because the realtime server speaks the Pusher protocol, the frontend uses Laravel Echo's `reverb` broadcaster with `pusher-js`, exactly as it would against Laravel Reverb:

```js
import Echo from 'laravel-echo';
import Pusher from 'pusher-js';

window.Pusher = Pusher;

window.Echo = new Echo({
    broadcaster: 'reverb',
    key: import.meta.env.VITE_REVERB_APP_KEY,
    wsHost: import.meta.env.VITE_REVERB_HOST,
    wsPort: import.meta.env.VITE_REVERB_PORT,
    wssPort: import.meta.env.VITE_REVERB_PORT,
    forceTLS: import.meta.env.VITE_REVERB_SCHEME === 'https',
    enabledTransports: ['ws', 'wss'],
});
```

The `key` must be the server's `REVERB_APP_KEY`: the client connects to `/app/{key}`, and any other key is refused.

## Concept Overview

Events are broadcast over "channels". A channel is just a name, `broadcasting.Channel{Name: "..."}`, usually built with `broadcasting.NewChannel`:

- A **public** channel may be joined by anyone who knows its name. For example, flight delays might be broadcast on `flights.{number}`: a flight's status is public information, so anyone following the flight may hear about it.
- A **private** channel is a channel whose name starts with `private-`, such as `private-App.Models.User.42`. The realtime server only lets a client join it with a signature from your application's [authorization route](#authorizing-channels).

Channel names may contain at most 200 of the characters `-a-zA-Z0-9_=@,.;`.

> [!NOTE]
> Presence channels (`presence-…`) and cache channels (`cache-…`, `private-cache-…`) are not supported: the realtime server refuses those subscriptions, and the authorization route answers 403 for a presence channel. Model broadcasting and broadcast notifications don't exist either; broadcast an event of your own instead.

## Defining Broadcast Events

To broadcast an event, implement the `broadcasting.ShouldBroadcast` interface on it. It has three methods, all required: `BroadcastOn` returns the channels, `BroadcastAs` the event name, and `BroadcastWith` the payload. Here is a delay announcement:

```go
package events

import (
	"example.com/flights/app/models"
	"github.com/atobeach/reef/broadcasting"
	"github.com/atobeach/reef/clock"
)

// FlightDelayed is broadcast when a flight's estimated departure slips
// past its scheduled one.
type FlightDelayed struct {
	Flight  models.Flight
	Minutes int
}

var _ broadcasting.ShouldBroadcast = FlightDelayed{}

// BroadcastOn is the public `flights.{number}` channel.
func (e FlightDelayed) BroadcastOn() []broadcasting.Channel {
	return []broadcasting.Channel{broadcasting.NewChannel("flights." + e.Flight.Number)}
}

// BroadcastAs is the event name; the frontend listens for
// `.flight.delayed`.
func (FlightDelayed) BroadcastAs() string {
	return "flight.delayed"
}

// FlightDelayedPayload is the broadcast payload the frontend reads.
type FlightDelayedPayload struct {
	FlightNumber string  `json:"flight_number"`
	DepartsAt    *string `json:"departs_at"`
	DelayMinutes int     `json:"delay_minutes"`
}

// BroadcastWith is the payload.
func (e FlightDelayed) BroadcastWith() any {
	var departsAt *string
	if e.Flight.EstimatedDepartsAt != nil {
		iso := clock.ISOString(*e.Flight.EstimatedDepartsAt)
		departsAt = &iso
	}

	return FlightDelayedPayload{
		FlightNumber: e.Flight.Number,
		DepartsAt:    departsAt,
		DelayMinutes: e.Minutes,
	}
}
```

The dispatcher finds `ShouldBroadcast` by type assertion, so an event with a slipped method signature would simply never be published. The `var _` line beside the type, on the value the event is dispatched as, turns that slip into a build failure.

To broadcast on a private channel, give the channel its `private-` name:

```go
func (e BookingConfirmed) BroadcastOn() []broadcasting.Channel {
	return []broadcasting.Channel{
		broadcasting.NewChannel("private-App.Models.User." + strconv.FormatInt(e.UserID, 10)),
	}
}
```

### Broadcast Name

Unlike Laravel, reef has no default broadcast name: `BroadcastAs` is the exact event name clients listen for. Because Echo would otherwise prefix the name with a PHP namespace, the client listens for it with a leading dot:

```js
Echo.channel(`flights.${flightNumber}`).listen('.flight.delayed', (payload) => {
    // ...
});
```

### Broadcast Data

`BroadcastWith` returns the payload, which reef encodes to JSON with [`EncodeASCIIJSON`](#encodeasciijson). Return a struct rather than a map: a struct's fields encode in declaration order, while a map's keys come out sorted, and your frontend and any recorded fixtures will expect one fixed order.

The payload travels inside the Pusher event's `data` field as a JSON *string*, which `pusher-js` parses again before your listener receives it as an object.

### Broadcast Queue

Broadcasts are never queued. The events dispatcher publishes a broadcastable event inline, right after its listeners have run. If a broadcast shouldn't hold up a request, dispatch the event from a [queued job](queues.md) or a [scheduled command](scheduling.md). The delay announcements above, for instance, are dispatched from a scheduled command, which already runs in the background.

The same goes for Laravel's `afterCommit`: the dispatcher publishes the moment you call it, so dispatch after your [transaction](database.md) commits.

### Broadcast Conditions

There is no `broadcastWhen`. An event whose `BroadcastOn` returns no channels is simply not broadcast, so you may return `nil` from `BroadcastOn` when the event shouldn't go out.

## Authorizing Channels

Private channels require you to authorize that the currently authenticated user can actually listen on the channel. When Echo subscribes to a private channel, it POSTs the channel name and its socket ID to your application's `/broadcasting/auth` route; your application decides, and answers with a signature the realtime server checks before letting the client in.

### Registering the Authorization Route

`broadcasting.Routes` registers `GET` and `POST /broadcasting/auth` on a router and returns the `*web.Route`, so you may name it. It takes the router, a function that defines your channel rules, the `*broadcasting.Pusher` that signs, and a function that resolves the subscribing user. A typical application registers it in its route file:

```go
// The api group, not web: the realtime client's subscription POST
// carries no CSRF token.
broadcasting.Routes(r.Middleware(d.Middleware.API...), Channels, d.BroadcastSigner, d.BroadcastUser).Name("broadcasting.auth")
```

The route reads `channel_name` and `socket_id` as text from the request [input](requests.md#retrieving-string-input-values), so they may arrive in the query string, a form body (which is what pusher-js sends) or a JSON body. A numeric `socket_id` in a JSON body is read as its digits, so `1234.5678` signs exactly as `"1234.5678"` would. The route then:

1. Reflashes the [session's](session.md) flash data, so a subscription request never consumes the values flashed for the next page.
2. Answers an empty `200` when the signer is `nil`. Pass `nil` for the `log` and `null` drivers, so the frontend's subscriptions quietly succeed while nothing is being broadcast.
3. Calls your user function. An error it returns becomes a 500.
4. Answers `403` for a `presence-` channel, a channel whose name doesn't start with `private`, or a channel your rules deny.
5. Otherwise signs the subscription and answers `{"auth": "app-key:signature"}`. A malformed socket ID (anything but `digits.digits`) is an error, a 500, rather than a 403: pusher-js always sends a well-formed one.

The user function returns a `broadcasting.User`, an interface with a single `AuthID() int64` method. Return an untyped `nil` for a guest. You can build it from the [auth guard](authentication.md) when your application boots:

```go
BroadcastUser: func(c *web.Context) (broadcasting.User, error) {
	user, ok, err := a.Auth.User(c)
	if err != nil {
		return nil, fmt.Errorf("resolving the subscribing user: %w", err)
	}

	// A guest is a nil User, never the zero models.User.
	if !ok {
		return nil, nil
	}

	return user, nil
},
```

> [!WARNING]
> Mount the route behind your session and authentication middleware: a signature is only as trustworthy as the user it is issued to. If your frontend sends the subscription POST without a CSRF token, mount the route in a group without [CSRF protection](csrf.md).

### Defining Authorization Callbacks

Your channels function receives a `*broadcasting.Channels` and registers one rule per channel pattern with its `Channel` method. A typical application defines its rules in `routes/channels.go`:

```go
package routes

import (
	"context"
	"strconv"

	"github.com/atobeach/reef/broadcasting"
)

// Channels are the private-channel authorisation rules broadcasting/auth
// checks.
func Channels(c *broadcasting.Channels) {
	c.Channel("App.Models.User.{id}", func(_ context.Context, user broadcasting.User, params []string) bool {
		id, err := strconv.ParseInt(params[0], 10, 64)

		return user != nil && err == nil && user.AuthID() == id
	})
}
```

A rule's callback is a `broadcasting.Authorizer`: it receives the request's context, the user, and the pattern's `{placeholder}` values as strings, in order, and returns whether the user may join. A few things to keep in mind:

- Patterns are written **without** the `private-` prefix: `private-App.Models.User.42` is matched against `App.Models.User.{id}`. The prefixes `private-encrypted-`, `private-` and `presence-` are stripped before matching.
- Each `{placeholder}` matches one segment containing no dot. A pattern may have several: `bookings.{booking}.passengers.{passenger}` hands the callback `[]string{booking, passenger}`.
- The first rule whose pattern matches decides. A channel no rule matches is denied.
- A guest (a `nil` user) is denied on any `private-` or `presence-` channel before the rules run, so a callback only sees `nil` for other channels. An empty channel name is always denied.

> [!NOTE]
> A pattern is turned into a regular expression without escaping, so a `.` in it matches any character. Plain dotted names like `App.Models.User.{id}` are unaffected, but avoid other regular-expression characters in patterns.

You may check your rules directly with `Allowed`, which applies them exactly as the route does:

```go
rules := &broadcasting.Channels{}
routes.Channels(rules)

if !rules.Allowed(ctx, user, "private-App.Models.User.42") {
	// ...
}
```

Laravel's channel classes don't exist; a rule is always a function.

## Broadcasting Events

Once an event implements `ShouldBroadcast`, you dispatch it like any other [event](events.md). An `events.Dispatcher` built with a broadcaster runs the event's listeners in order and then publishes it:

```go
dispatcher := events.NewDispatcher(pusher)

err := dispatcher.Dispatch(ctx, appevents.FlightDelayed{Flight: flight, Minutes: 30})
```

`Dispatch` returns the first listener error, in which case nothing is broadcast, or else the broadcaster's error. A dispatcher built with a `nil` broadcaster uses `broadcasting.Null`.

If you'd rather publish without the dispatcher, call `broadcasting.Broadcast` with a broadcaster and the event. An event with no channels is a no-op:

```go
err := broadcasting.Broadcast(ctx, broadcaster, appevents.FlightDelayed{Flight: flight, Minutes: 30})
```

There is no "only to others": the publisher never sends a socket ID, so every subscriber of the channel receives the event.

### Customizing the Connection

There is no default broadcaster hidden behind a facade: the broadcaster is whatever value you hand to `events.NewDispatcher` or `broadcasting.Broadcast`. To publish through a different connection, pass a different `Broadcaster`.

### Anonymous Events

To broadcast without defining an event type, call the broadcaster directly with the channel names, the event name, and the payload:

```go
err := broadcaster.Broadcast(ctx, []string{"flights." + flight.Number}, "flight.gate-changed", map[string]string{
	"flight_number": flight.Number,
	"gate":          gate,
})
```

`Pusher.Broadcast` encodes the payload once and publishes it in chunks of 100 channels, the Pusher HTTP API's per-event limit. It returns an error for a channel name that isn't [valid](#validchannelname), and for any answer from the server other than `200`; the error includes up to 2 KB of the server's reply.

### Rescuing Broadcasts

A broadcast that fails returns its error from `Dispatch`, and it's up to you whether that fails the caller. The `flights:check-delays` command above, for example, could report the error and carry on with the rest of the batch, leaving the failed flight unannounced so it is retried on the next run:

```go
if err := cmd.Events.Dispatch(c, appevents.FlightDelayed{Flight: flight, Minutes: delay}); err != nil {
	c.Report(err)

	continue
}
```

## Receiving Broadcasts

Once Echo is configured, listen on a public channel with `channel` and on a private one with `private`. Echo adds the `private-` prefix, and authorizes the subscription against `/broadcasting/auth`:

```js
Echo.channel(`flights.${flightNumber}`)
    .listen('.flight.delayed', (payload) => {
        console.log(payload.delay_minutes);
    });

Echo.private(`App.Models.User.${userId}`)
    .listen('.booking.confirmed', (payload) => {
        // ...
    });
```

To stop listening, leave the channel with `Echo.leave('flights.' + flightNumber)`.

## Client Events

Sometimes you may wish to send an event to other connected clients without hitting your application at all, such as a "typing" indicator. The realtime server relays these "client events" (Echo's `whisper`, which sends a `client-` prefixed event) to the channel's other subscribers, never back to the sender:

```js
Echo.private(`chat.${roomId}`).whisper('typing', { name: user.name });
```

Who may send them is the app's `AcceptClientEventsFrom` setting:

- `"members"`, the default: only a connection subscribed to the channel. Anyone else gets a `4009` error.
- `"all"`: any connection.
- Anything else, such as `"none"`: client events are refused with a `4301` error.

A client event must name its channel, and its data must be absent, `null`, an object or an array; anything else is answered with a `4200` error. A well-formed event whose name starts with neither `pusher:` nor `client-` is dropped silently.

## The Realtime Server

The `realtime` package is a WebSocket server that speaks the Pusher Channels protocol (version 7, the one `pusher-js` uses) together with the Pusher HTTP API that publishers trigger events through. Frames, error codes and signature checks follow the Pusher protocol, so an unmodified `pusher-js` client and any Pusher publisher work against it. One process serves one Pusher app.

### Running the Server

The server runs as a console command, `reverb:start`, so deploy scripts written for Laravel Reverb keep working. `realtime.StartCommand` builds it from a `realtime.Config`; register it with your [console kernel](console.md):

```go
kernel.Register(realtime.StartCommand(cfg.Realtime))
```

```shell
flights reverb:start
flights reverb:start --host=[::] --port=8080 --path=/realtime
```

The `--host`, `--port` and `--path` options override the config's `Host`, `Port` and `Path`; a `--port` that isn't a whole number of at least 1 is a usage error. The command fails with `REVERB_APP_ID, REVERB_APP_KEY and REVERB_APP_SECRET must be set.` when any of the app's credentials is empty. Otherwise it listens (an address already in use fails the command), prints `Starting server on {address}{path}`, and serves until the command's context ends; an entry point typically cancels it on `SIGINT` or `SIGTERM`. On the way out every client gets the frame being written and those queued for it, then close code `1001`, which makes `pusher-js` reconnect, to another server during a deploy.

> [!NOTE]
> The host defaults to `127.0.0.1`, never `0.0.0.0`. To listen on every interface you must say so, for example with `--host=[::]` inside a container; the brackets are accepted.

### Server Configuration

`realtime.Config` holds the listen address and server-wide limits, and `realtime.App` the one app's credentials and per-app limits. Any field left at zero (or, for a limit, negative) takes its default, so a config with only the app's credentials is a working loopback server:

```go
cfg := realtime.Config{
	App: realtime.App{ID: "flights", Key: "app-key", Secret: "app-secret"},
}
```

| `Config` field | Default | Description |
| --- | --- | --- |
| `Host` | `127.0.0.1` | Where `reverb:start` listens. |
| `Port` | `8080` | Where `reverb:start` listens. |
| `Path` | `""` | A prefix for every route, such as `/realtime`. A trailing slash is trimmed. |
| `MaxRequestSize` | `10000` | The largest HTTP API request body, in bytes. |
| `App` | | The app; see below. |
| `SendBuffer` | `128` | How many frames may queue for one connection. A connection whose queue is full is treated as a slow consumer and closed with `4100`, so a stalled client can't grow the server's memory. |
| `WriteTimeout` | `10s` | How long one frame write may take before the connection is closed. |
| `MaxSubscriptions` | `100` | How many channels one connection may join. Pusher has no such limit, but without one a single client could create unbounded channel entries. |
| `CheckInterval` | `60s` | How often silent connections are pinged and stale ones pruned. |

| `App` field | Default | Description |
| --- | --- | --- |
| `ID`, `Key`, `Secret` | | The Pusher app's credentials. Clients connect with the key; publishers sign with the key and secret. |
| `AllowedOrigins` | `["*"]` | Host patterns a WebSocket's `Origin` header must match. `*` matches any run of characters, dots included (`*.example.com`); `*` on its own allows any origin. |
| `PingInterval` | `60s` | How long a connection may stay silent before the server pings it. |
| `ActivityTimeout` | `30s` | Sent to the client in `pusher:connection_established`: the client pings after this much silence. |
| `MaxConnections` | `0` | The cap on concurrent connections; `0` means no limit. |
| `MaxMessageSize` | `10000` | The largest client frame, in bytes. A bigger one closes the connection with `1009`. |
| `AcceptClientEventsFrom` | `"members"` | Who may send [client events](#client-events): `"members"`, `"all"`, or anything else to refuse them. |
| `RateLimit` | disabled | A `realtime.RateLimit`; see below. |

| `RateLimit` field | Default | Description |
| --- | --- | --- |
| `Enabled` | `false` | Whether client messages are rate limited at all. |
| `MaxAttempts` | `60` | Messages one connection may send per window. |
| `Decay` | `60s` | The window's length. |
| `Terminate` | `false` | Close the connection on the limit (code `4301`) instead of only answering a `4301` error. |

> [!WARNING]
> When `AllowedOrigins` lists patterns, a connection is matched on the host of its `Origin` header, so a client that sends no `Origin` at all (a native app or a script) is refused. Use `*` if such clients must connect.

An application may map Laravel Reverb's environment variables onto the config in its `config` package. Note that this example caps connections at 10,000 when `REVERB_APP_MAX_CONNECTIONS` is unset, rather than leaving them unlimited:

```go
func loadRealtime() realtime.Config {
	return realtime.Config{
		Host:           env.Get("REVERB_SERVER_HOST", "127.0.0.1"),
		Port:           env.Int("REVERB_SERVER_PORT", 8080),
		Path:           env.Get("REVERB_SERVER_PATH", ""),
		MaxRequestSize: int64(env.Int("REVERB_MAX_REQUEST_SIZE", 10_000)),
		App: realtime.App{
			ID:                     env.Get("REVERB_APP_ID", ""),
			Key:                    env.Get("REVERB_APP_KEY", ""),
			Secret:                 env.Get("REVERB_APP_SECRET", ""),
			AllowedOrigins:         env.List("REVERB_APP_ALLOWED_ORIGINS", "*"),
			PingInterval:           time.Duration(env.Int("REVERB_APP_PING_INTERVAL", 60)) * time.Second,
			ActivityTimeout:        time.Duration(env.Int("REVERB_APP_ACTIVITY_TIMEOUT", 30)) * time.Second,
			MaxConnections:         env.Int("REVERB_APP_MAX_CONNECTIONS", 10_000),
			MaxMessageSize:         int64(env.Int("REVERB_APP_MAX_MESSAGE_SIZE", 10_000)),
			AcceptClientEventsFrom: env.Get("REVERB_APP_ACCEPT_CLIENT_EVENTS_FROM", "members"),
			RateLimit: realtime.RateLimit{
				Enabled:     env.Bool("REVERB_APP_RATE_LIMITING_ENABLED", false),
				MaxAttempts: env.Int("REVERB_APP_RATE_LIMIT_MAX_ATTEMPTS", 60),
				Decay:       time.Duration(env.Int("REVERB_APP_RATE_LIMIT_DECAY_SECONDS", 60)) * time.Second,
				Terminate:   env.Bool("REVERB_APP_RATE_LIMIT_TERMINATE", false),
			},
		},
		SendBuffer:       env.Int("REVERB_SEND_BUFFER", 128),
		WriteTimeout:     time.Duration(env.Int("REVERB_WRITE_TIMEOUT", 10)) * time.Second,
		MaxSubscriptions: env.Int("REVERB_MAX_SUBSCRIPTIONS", 100),
	}
}
```

### Embedding the Server

If you'd rather run the server from your own code, `realtime.Serve` serves on a listener until its context ends, then drains: the HTTP side stops accepting (with a 10 second grace period) and every WebSocket gets what is queued for it, then `1001`. `realtime.Address` joins a host and port, accepting a bracketed IPv6 host such as `[::]`:

```go
var lc net.ListenConfig

listener, err := lc.Listen(ctx, "tcp", realtime.Address(cfg.Host, cfg.Port))
if err != nil {
	return err
}

return realtime.Serve(ctx, listener, cfg, nil)
```

For full control, `realtime.NewServer` builds a `*realtime.Server`, which is an `http.Handler`. Run its maintenance loop with `Run`, mount it wherever you like, and disconnect everyone with `Close` when you shut down:

```go
server := realtime.NewServer(cfg, nil)

go func() { _ = server.Run(ctx) }()

httpServer := &http.Server{Addr: "127.0.0.1:8080", Handler: server, ReadHeaderTimeout: 10 * time.Second}

go func() {
	<-ctx.Done()
	_ = httpServer.Shutdown(context.WithoutCancel(ctx))
	server.Close(10 * time.Second)
}()

return httpServer.ListenAndServe()
```

The server's methods:

- `ServeHTTP` routes the Pusher endpoints under the configured `Path`: `GET /up` (answers `{"health":"OK"}`), the WebSocket at `GET /app/{key}`, and the HTTP API's `POST /apps/{id}/events` and `POST /apps/{id}/batch_events`. Any other path is a `404`, and a wrong method a `405` with an `Allow` header.
- `Run` pings silent connections and prunes stale ones every `CheckInterval` and, when the server has a [fanout](#scaling-across-instances), delivers what other instances publish, until its context ends.
- `Publish` sends a `realtime.Message` to every subscriber of its channels, through the fanout when one is set.
- `Connections` is the number of open connections.
- `Close` sends each client the frame being written and those queued for it (each within `WriteTimeout`), then disconnects it with `1001`, and waits up to the given timeout for them to go. Connections that arrive while it is closing are refused with `4200`.

You may publish from inside the server's own process without going through the HTTP API:

```go
err := server.Publish(ctx, realtime.Message{
	Channels: []string{"flights.FL123"},
	Event:    "flight.delayed",
	Data:     `{"flight_number":"FL123","delay_minutes":30}`,
})
```

A `realtime.Message` is one published event:

| Field | Description |
| --- | --- |
| `Channels` | The channels it goes to. Connections not subscribed to them get nothing. |
| `Event` | The event name. |
| `Data` | An HTTP API event's `data`: the payload's JSON as a string, which `pusher-js` parses again. |
| `ClientData` | A client event's `data`, the raw JSON the client sent. Set only for client events. |
| `ExceptSocketID` | A connection to skip: the HTTP API's `socket_id`, or a client event's sender. |

### Scaling Across Instances

A single server process holds every connection, and publishes are delivered in-process. To run several instances behind a load balancer, give the server a `realtime.Fanout`, which carries each published message to every instance, the publishing one included; each instance then delivers it to its own connections. Its two methods are `Publish(ctx, Message) error` and `Subscribe(ctx, deliver func(Message)) error`, which calls `deliver` for every message any instance publishes until its context ends. A `nil` fanout means in-process delivery.

reef doesn't ship a fanout. One over Valkey pub/sub is a few lines, since `Message` carries its own JSON tags:

```go
// ValkeyFanout carries published messages to every realtime instance over
// one Valkey pub/sub channel.
type ValkeyFanout struct {
	Client  *redis.Client
	Channel string
}

// Publish implements realtime.Fanout.
func (f ValkeyFanout) Publish(ctx context.Context, m realtime.Message) error {
	payload, err := json.Marshal(m)
	if err != nil {
		return fmt.Errorf("encoding the realtime message: %w", err)
	}

	return f.Client.Publish(ctx, f.Channel, payload).Err()
}

// Subscribe implements realtime.Fanout.
func (f ValkeyFanout) Subscribe(ctx context.Context, deliver func(realtime.Message)) error {
	subscription := f.Client.Subscribe(ctx, f.Channel)
	defer subscription.Close()

	for {
		received, err := subscription.ReceiveMessage(ctx)
		if err != nil {
			return err
		}

		var m realtime.Message
		if json.Unmarshal([]byte(received.Payload), &m) == nil {
			deliver(m)
		}
	}
}
```

Then pass it to `realtime.Serve` or `realtime.NewServer` in place of `nil`. `Run` starts the subscription; if it ends with an error before the context does, the server logs it. `MaxConnections` stays a per-instance limit.

> [!NOTE]
> `reverb:start` always serves with a `nil` fanout. To run several instances, start the server through `realtime.Serve` from a command of your own.

### Connection Lifecycle and Error Codes

When a client connects to `/app/{key}`, the server upgrades the connection first and then checks it, so a refused client receives a Pusher error frame and a close code `pusher-js` understands rather than a bare HTTP error. An accepted client receives `pusher:connection_established` with its socket ID and the activity timeout in seconds.

Any frame from the client counts as activity. Every `CheckInterval`, a connection that has been silent for a whole `PingInterval` is sent `pusher:ping`; one that is still silent at the next check is sent a `4201` error and closed. Clients may ping too: `pusher:ping` is answered with `pusher:pong`.

These are the errors the server sends. `pusher-js` stops reconnecting on a close code from 4000 to 4099, reconnects after a back-off from 4100 to 4199, and reconnects at once from 4200 to 4299.

| Code | Message | When | Closes? |
| --- | --- | --- | --- |
| `4001` | Application does not exist | The key in `/app/{key}` isn't the app's. | Yes |
| `4004` | Application is over connection quota | `MaxConnections` reached. | Yes |
| `4009` | Origin not allowed | The `Origin` doesn't match `AllowedOrigins`. | Yes |
| `4009` | Connection is unauthorized | A private subscription's signature is missing or wrong. | No |
| `4009` | Presence channels are not supported | A `presence-` subscription. | No |
| `4009` | Cache channels are not supported | A cache-channel subscription. | No |
| `4009` | The client is not a member of the specified channel. | A client event on a channel the sender hasn't joined, under `"members"`. | No |
| `4100` | Too many subscriptions | `MaxSubscriptions` reached. | No |
| `4100` | Over capacity | The connection's send queue is full (no error frame is sent). | Yes |
| `4200` | Invalid message format | An unreadable frame, an unknown `pusher:` event, or an invalid channel name. | No |
| `4200` | Server shutting down | A connection arriving while the server closes. | Yes |
| `4201` | Pong reply not received in time | A pinged connection stayed silent. | Yes |
| `4301` | Rate limit exceeded | The connection's `RateLimit` was exceeded. | Only with `Terminate` |
| `4301` | The app does not have client messaging enabled. | A client event when they're refused. | No |
| `1001` | Server shutting down | `Close`, `Serve` or `reverb:start` shutting down. | Yes |
| `1009` | | A frame larger than `MaxMessageSize`. | Yes |

A private subscription must carry `auth` in the form `key:signature`, where the key is the app's and the signature is [`ChannelSignature`](#channelsignature) of the socket ID and channel name. That is exactly what the [authorization route](#registering-the-authorization-route) returns. Subscribing to a channel the connection has already joined succeeds again without counting twice.

### The HTTP API

Publishers trigger events through two endpoints of the Pusher HTTP API. `broadcasting.Pusher` uses the first; any other Pusher server library works against both:

- `POST /apps/{id}/events` publishes one event, `{"name": ..., "data": ..., "channel": ...}` or with `"channels": [...]` (at most 100), and an optional `socket_id` to skip. It answers `{}`.
- `POST /apps/{id}/batch_events` publishes up to 10 events, `{"batch": [{"name": ..., "data": ..., "channel": ...}, ...]}`, each to one channel and each with its own optional `socket_id`. Every event is validated before any is published. It answers `{"batch":{}}`.

Every request must be signed the way Pusher signs them (see [`SignedQuery`](#signedquery)): `auth_key` must be the app's key, `auth_signature` must match, `auth_timestamp` must be within 600 seconds of the server's clock, and a `body_md5` that is sent must match the body. The server answers:

| Status | When |
| --- | --- |
| `200` | The event was published. |
| `400` | The body couldn't be read or isn't JSON. |
| `401` | The signature is invalid (`Authentication signature invalid.`). |
| `404` | The app ID isn't the server's (`No matching application for ID [id].`), or the path is unknown. |
| `413` | The body is larger than `MaxRequestSize`. |
| `422` | A field is missing or invalid: `{"field": ["message", ...]}`, fields in the order they failed. |
| `500` | The fanout failed to publish. |

The optional `info` field is accepted but ignored: channel occupancy is never returned.

## Testing

When testing code that broadcasts, use a `*broadcasting.Fake` as the broadcaster. It records each broadcast as a `broadcasting.Sent` (its `Channels`, `Event`, and `Payload` encoded exactly as the publisher would encode it), which `Sent()` returns in order. A [test case](testing.md#a-full-test-case) of your own may wire one in, as `tc.Broadcasts` below, and the `broadcastingtest` package asserts on it:

```go
func TestBroadcastsTheDelay(t *testing.T) {
	tc := tests.Setup(t)
	tc.FreezeTime()

	// ...

	tc.RunCommand("flights:check-delays").AssertSuccessful()

	broadcastingtest.AssertBroadcastCount(t, tc.Broadcasts, 1)
	broadcastingtest.AssertBroadcastOn(t, tc.Broadcasts, "flights."+flight.Number, "flight.delayed")
}
```

To simulate the realtime server rejecting a broadcast, set the fake's `Fail` function. It is asked about every broadcast; when it returns an error, the broadcast fails with it and isn't recorded:

```go
fake := &broadcasting.Fake{
	Fail: func(channels []string, event string) error {
		return errors.New("connection refused")
	},
}
```

The `broadcastingtest` assertions (`AssertBroadcastOn`, `AssertBroadcastTimes`, `AssertBroadcastCount` and `AssertNothingBroadcast`) are documented with the other fakes in [mocking](mocking.md).

To test your [channel rules](#defining-authorization-callbacks), send the subscription request to `/broadcasting/auth` the way Echo would, and compare the answer with what a `Pusher` holding the same credentials signs. A feature test can boot the app on the `reverb` connection, so the mounted route signs as it does when deployed (`tests.WithConfig` and `tc.ActingAs` are your own test case's helpers):

```go
func TestTheMountedBroadcastingAuthSignsTheUsersOwnPrivateChannel(t *testing.T) {
	tc := tests.Setup(t, tests.WithConfig(func(cfg *config.Config) {
		cfg.Broadcasting.Connection = "reverb"
		cfg.Broadcasting.Reverb = config.Reverb{AppID: "1", Key: "app-key", Secret: "app-secret", Host: "localhost", Port: 8080, Scheme: "http"}
	}))
	pusher := broadcasting.NewPusher(broadcasting.PusherConfig{AppID: "1", Key: "app-key", Secret: "app-secret"})
	user := factories.User(tc).Create()

	channel := fmt.Sprintf("private-App.Models.User.%d", user.ID)
	want, _ := pusher.AuthorizeChannel(channel, "1234.5678")

	browser := tc.ActingAs(user).WithoutCSRFToken()

	browser.PostJSON("/broadcasting/auth", map[string]any{"channel_name": channel, "socket_id": "1234.5678"}).
		AssertOk().
		AssertJSON(map[string]any{"auth": want["auth"]})

	browser.PostJSON("/broadcasting/auth", map[string]any{"channel_name": fmt.Sprintf("private-App.Models.User.%d", user.ID+1), "socket_id": "1234.5678"}).
		AssertForbidden()
}
```

The HTTP testing helpers used here are described in [HTTP tests](http-tests.md).

## Pusher Protocol Helpers

The publisher and the realtime server share these helpers, which implement the Pusher wire format. You'll rarely need them directly, but they're useful when talking to a Pusher-compatible server from your own code or when checking a signature in a test.

#### `EncodeASCIIJSON`

The wire encoding of every broadcast payload and realtime frame: standard JSON with slashes escaped as `\/` and every non-ASCII character as a `\uXXXX` escape (surrogate pairs above U+FFFF), while `<`, `>` and `&` are left alone. This is PHP's `json_encode` default, which is what Laravel's publishers send. The bytes are part of the contract, since the HTTP API signs the body's MD5:

```go
body, err := broadcasting.EncodeASCIIJSON(map[string]string{"city": "Málaga/Spain"})
// {"city":"M\u00e1laga\/Spain"}
```

#### `SignedQuery`

A complete Pusher HTTP API query string: `auth_key`, `auth_timestamp` and `auth_version=1.0` merged with your parameters, sorted by key, then `auth_signature` appended. Values are signed raw and URL-encoded only on the wire:

```go
sum := md5.Sum(body)

query := broadcasting.SignedQuery("app-key", "app-secret", "POST", "/apps/flights/events", map[string]string{
	"body_md5": hex.EncodeToString(sum[:]),
}, time.Now().Unix())
```

#### `RequestSignature`

An HTTP API request's `auth_signature`: the parameters sorted by key and joined as `k=v&k=v`, then the hex HMAC-SHA256 of `METHOD\npath\nquery` with the app secret. `SignedQuery` signs with it and the realtime server verifies with it.

#### `ChannelSignature`

The hex HMAC-SHA256 of `socket_id:channel` (with `:channel_data` appended when it isn't empty) with the app secret: the part after the key in a private subscription's `auth`.

```go
signature := broadcasting.ChannelSignature("app-secret", "1234.5678", "private-App.Models.User.42", "")
```

#### `ValidChannelName`

Reports whether a name is a valid Pusher channel name: at most 200 of the characters `-a-zA-Z0-9_=@,.;`.

#### `Pusher.Trigger`

Publishes one event: a signed `POST /apps/{id}/events` whose `data` is the string you pass, sent with `Content-Type: application/json` and `X-Pusher-Library: reef`. The HTTP API takes at most 100 channels per event, which is why `Pusher.Broadcast` encodes the payload and calls `Trigger` once per 100 channels. The `auth_timestamp` comes from the [clock](helpers.md) on the context, so a test that freezes time signs deterministic requests.

```go
err := pusher.Trigger(ctx, []string{"flights.FL123"}, "flight.delayed", `{"flight_number":"FL123"}`)
```

#### `Pusher.AuthorizeChannel`

The `{"auth": "key:signature"}` map the authorization route returns for a private subscription. It returns an error for a channel name with a character outside the Pusher alphabet, and `broadcasting.ErrInvalidSocketID` for a socket ID that isn't `digits.digits`:

```go
auth, err := pusher.AuthorizeChannel("private-App.Models.User.42", "1234.5678")
if errors.Is(err, broadcasting.ErrInvalidSocketID) {
	// ...
}
```
