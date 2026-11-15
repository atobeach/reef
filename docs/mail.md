# Mail

- [Introduction](#introduction)
    - [Configuration](#configuration)
    - [Transport Prerequisites](#transport-prerequisites)
- [Writing Mailables](#writing-mailables)
    - [Configuring the Sender](#configuring-the-sender)
    - [Configuring the View](#configuring-the-view)
    - [View Data](#view-data)
- [Sending Mail](#sending-mail)
    - [Queueing Mail](#queueing-mail)
    - [Queued Mail and Secrets](#queued-mail-and-secrets)
    - [Hydrating Queued Mailables](#hydrating-queued-mailables)
    - [Retrying Queued Mail](#retrying-queued-mail)
- [Rendering Mailables](#rendering-mailables)
- [Testing Mailables](#testing-mailables)
- [Mail and Local Development](#mail-and-local-development)
- [Custom Transports](#custom-transports)
- [Available Methods](#available-methods)

## Introduction

Sending email doesn't have to be complicated. reef's mail package (`github.com/atobeach/reef/mail`) renders each email from a "mailable", a small Go type that knows its subject and how to render its HTML, and hands the result to a transport: an email API such as Bento's, the log while you develop, or an in-memory fake in tests.

```go
err := mailer.To(user.Email).Send(ctx, appmail.NewWelcomeMail(user))
```

### Configuration

The mailer is built once, at boot, with `mail.NewMailer` and a `mail.MailerConfig`. A typical application builds it like this:

```go
a.Mail = mail.NewMailer(mail.MailerConfig{
	Transport: mailTransport(cfg, conns.Mail),
	From:      cfg.Mail.FromAddress,
	Views:     views.New(cmp.Or(cfg.App.AssetURL, cfg.App.URL)),
	Queue:     a.Queue,
	Workers:   a.Workers,
	// The queued mailables the worker may rebuild.
	Queueable: []mail.ShouldQueue{
		appmail.BookingConfirmationMail{},
		appmail.WelcomeMail{},
		appmail.ResetPasswordMail{},
		appmail.VerifyEmailMail{},
	},
	// Queued mail args (reset links, codes, passenger names) sit in the
	// queue table until swept, so they are sealed with the app key.
	Encrypter: a.Encrypter,
})
```

| Field | Purpose |
|---|---|
| `Transport` | Delivers rendered messages. |
| `From` | The default sender, typically read from `MAIL_FROM_ADDRESS`. |
| `Views` | The templates mailables render with: any `mail.Renderer`. |
| `Queue` | The [queue connection](queues.md) queued mailables are pushed to; without one, they are sent inline. |
| `Workers` | When set, `NewMailer` registers the handler that sends queued mail on this registry. |
| `Queueable` | One value of every queued mailable type, which a worker rebuilds from the queue by its `MailKind`. An empty kind, or one kind given to two types, panics. |
| `Encrypter` | Seals queued mail; usually the application's `*crypt.Encrypter`. |

> [!WARNING]
> `NewMailer` registers its queued-mail handler on `Workers`, and a job kind may only be registered once: build one mailer per worker registry.

### Transport Prerequisites

A transport is anything implementing `mail.Transport`. reef includes three, and your application may pick between them with an environment variable such as `MAIL_MAILER`:

| Transport | Delivers by |
|---|---|
| `*mail.BentoTransport` | Bento's transactional email API. |
| `mail.LogTransport` | Logging each message (`MAIL_MAILER=log`, the default). |
| `*mail.Fake` | Recording each message, for [tests](#testing-mailables). |

To use Bento, build the transport with your keys and site:

```go
transport := mail.NewBentoTransport(mail.BentoConfig{
	PublishableKey: cfg.Mail.BentoPublic,
	SecretKey:      cfg.Mail.BentoSecret,
	SiteUUID:       cfg.Mail.BentoSiteUUID,
})
```

The transport authenticates with HTTP basic auth (the publishable key as the user, the secret key as the password) and gives each send 30 seconds. Each message is one `POST https://app.bentonow.com/api/v1/batch/emails?site_uuid=...` (the `mail.SendBentoEmails` request) carrying the sender, the comma-joined recipients, the subject and the HTML body, marked transactional. A 401 comes back as an error naming `BENTO_PUBLISHABLE_KEY` and `BENTO_SECRET_KEY`, and any other failed response as the [HTTP client's](http-client.md) status error. `BentoConfig.Mock` answers Bento's API from an `*httpclient.MockClient` instead of the network, for tests.

## Writing Mailables

Mailables usually live in an `app/mail` package. A mailable is any type implementing `mail.Mailable`, with two methods: `Envelope`, which returns its subject (and, optionally, its sender), and `Content`, which renders its HTML body:

```go
package mail

import (
	"context"

	"github.com/atobeach/reef/mail"
)

// ResetPasswordMail carries the frontend's reset link.
type ResetPasswordMail struct {
	URL string
}

// Envelope implements mail.Mailable.
func (ResetPasswordMail) Envelope() mail.Envelope {
	return mail.Envelope{Subject: "Reset Your Password"}
}

// Content implements mail.Mailable.
func (m ResetPasswordMail) Content(ctx context.Context, views mail.Renderer) (string, error) {
	return views.Render(ctx, "emails/reset-password", m)
}
```

reef's mailables carry no attachments, custom headers, tags, CC/BCC or plain-text alternative, and there are no Markdown mailables: a message is a sender, its recipients, a subject and an HTML body.

### Configuring the Sender

Every message is sent from the mailer's `From` address unless the mailable's envelope names another:

```go
func (InvoiceMail) Envelope() mail.Envelope {
	return mail.Envelope{Subject: "Your invoice", From: "billing@example.com"}
}
```

### Configuring the View

reef doesn't ship a template engine. `Content` receives the mailer's `Views`, a `mail.Renderer`, which renders a named template with data:

```go
type Renderer interface {
	Render(ctx context.Context, name string, data any) (string, error)
}
```

A renderer is usually `html/template` over a directory of templates. For example, a `views` package over `resources/views/emails` could render `resources/views/emails/reset-password.html` for `views.Render(ctx, "emails/reset-password", m)`. The context passed to `Render` carries the [clock](helpers.md), so a footer's year follows a test's frozen time. You may also build the HTML any other way: `Content` only has to return it.

### View Data

Whatever you pass to `Render` is the template's data. Usually that is the mailable itself, whose exported fields the template reads. When a template needs more than the mailable holds, pass a struct made for it:

```go
// Content renders the booking confirmation view.
func (m BookingConfirmationMail) Content(ctx context.Context, views mail.Renderer) (string, error) {
	return views.Render(ctx, "emails/booking-confirmation", struct {
		Booking   Booking
		AssetRoot string
		Year      int
	}{m.Booking, m.AssetRoot, clock.Now(ctx).Year()})
}
```

## Sending Mail

To send a message, call `To` on the mailer with one or more addresses, then `Send` with the mailable. `Send` renders the mailable and hands it to the transport, and returns the transport's error:

```go
if err := a.Mail.To(user.Email).Send(ctx, appmail.ResetPasswordMail{URL: link}); err != nil {
	return err
}
```

`To` returns a `*mail.Pending`, an addressed send waiting for its mailable; you may keep it and send several mailables to the same recipients.

### Queueing Mail

Since sending email can negatively impact the response time of your application, many developers choose to queue email messages for background sending. To queue a mailable, implement `mail.ShouldQueue` by adding a `MailKind` method, which names the mailable in the queue table; `Send` then pushes it onto the mailer's queue instead of sending it:

```go
var (
	_ mail.ShouldQueue = BookingConfirmationMail{}
	_ queue.Tries      = BookingConfirmationMail{}
	_ queue.Backoff    = BookingConfirmationMail{}
)

// MailKind implements mail.ShouldQueue (stored in the queue table: never
// change it). Queued, SMTP failures retry on the worker.
func (BookingConfirmationMail) MailKind() string { return "booking_confirmation" }
```

The `var` block is the compile-time check: `Send` finds `ShouldQueue` (and `queue.Tries` and `queue.Backoff`, below) by type assertion, so a method whose signature slipped would otherwise be silently skipped. With the assertion, it fails the build.

> [!WARNING]
> `MailKind` names the mailable in the queue table, like a job's `Kind`. Never change it once mail of that kind may be queued: a worker can't rebuild a mailable whose kind it no longer knows. Renaming or moving the Go type is safe.

The method is called `MailKind` rather than `Kind` so that a mailable never also satisfies `queue.Job`, and can't be handed to a queue's `Dispatch` by mistake.

Every queued mailable type must also be listed in the mailer's `Queueable`, so the worker knows how to rebuild it. A queued mailable travels as JSON, in a `mail.SendQueuedMailable` job: its kind, its fields and its recipients. The worker rebuilds it, renders it and sends it, so a template fix deployed between dispatch and send still applies. Its fields must therefore be exported and JSON-serializable, and you should send mailables as values rather than pointers.

`Pending.Queue` pushes a `ShouldQueue` mailable onto the queue directly:

```go
err := a.Mail.To(user.Email).Queue(ctx, appmail.VerifyEmailMail{OTP: code})
```

A few things to keep in mind:

- On a mailer without a `Queue`, `Send` sends a `ShouldQueue` mailable inline, while `Queue` fails with `mail.ErrNoQueue`.
- Queueing a type missing from `Queueable`, or one whose kind `Queueable` gives to another type, fails with `mail: <type> (kind "<kind>") is not queueable (MailerConfig.Queueable)`, such as `mail: mail.InvoiceMail (kind "invoice") is not queueable (MailerConfig.Queueable)`. Without the type check, a copied kind would have the worker render the other type's email from this payload.
- The job names the mailable by its kind, so a worker whose mailer no longer lists that kind fails the job. Keep a kind in `Queueable` until no job of it can still be queued.
- Under the `sync` queue connection, the job runs at once, so `Send` delivers before it returns.

When queueing shouldn't fail the request that triggered it (a registration mustn't fail because the mail provider is down), log the error rather than return it. A small helper keeps that in one place:

```go
// Queue sends a queued mailable, logging a failure rather than returning it:
// a registration must not 500 because the mail provider is down.
func Queue(ctx context.Context, mailer *mail.Mailer, to string, mailable mail.ShouldQueue) {
	if err := mailer.To(to).Send(ctx, mailable); err != nil {
		slog.ErrorContext(ctx, "mail: sending queued mailable failed", "mailable", mailable.MailKind(), "error", err)
	}
}
```

### Queued Mail and Secrets

A queued job's payload sits in the queue table until it is swept, and a mail's fields are often its secrets: a password-reset link, a verification code, a customer's details. On a mailer with an `Encrypter`, the mailable's payload and recipients travel only inside the job's `Sealed` field, encrypted with your application key (see [encryption](encryption.md)); the job's `Payload` and `To` fields stay empty.

The worker unseals the job before it rebuilds the mailable. `Mailer.Open(job)` does the same for you, returning the job with its payload and recipients decrypted, which is handy in tests:

```go
pushed := tc.FakeQueue()

// ... the request that queues the reset mail ...

jobs := queuetest.Pushed[mail.SendQueuedMailable](pushed)
if jobs[0].Mailable != "reset_password" || jobs[0].Sealed == "" || jobs[0].To != nil {
	t.Fatalf("queued %+v", jobs[0])
}

opened, err := tc.App.Mail.Open(jobs[0])
if err != nil {
	t.Fatal(err)
}

if opened.To[0] != user.Email {
	t.Fatalf("opened = %+v", opened)
}
```

A job queued without encryption opens as it is, so turning encryption on doesn't strand mail already queued. A sealed job reaching a mailer without an `Encrypter` fails.

### Hydrating Queued Mailables

Sometimes a mailable needs a record you'd rather not copy into the queue table, such as a user with a password hash. Have the mailable carry the record's id alone, and implement `mail.Hydrator`: the worker calls `Hydrate` after rebuilding the mailable, and sends the mailable it returns. Returning a `nil` mailable means the record is gone, and nothing is sent:

```go
// WelcomeMail greets a newly registered user. It is queued as the user's id
// alone and reloaded by the worker.
type WelcomeMail struct {
	UserID int64       `json:"userId"`
	User   models.User `json:"-"`
}

var (
	_ mail.ShouldQueue = WelcomeMail{}
	_ mail.Hydrator    = WelcomeMail{}
)

// Hydrate implements mail.Hydrator: a user deleted before the worker ran
// gets no mail.
func (m WelcomeMail) Hydrate(ctx context.Context) (mail.Mailable, error) {
	user, err := models.Users(ctx).Find(m.UserID)
	if err != nil {
		return nil, fmt.Errorf("loading user %d for the welcome mail: %w", m.UserID, err)
	}

	if user == nil {
		return nil, nil
	}

	m.User = *user

	return m, nil
}
```

### Retrying Queued Mail

A queued mail gets the queue's default number of attempts. A mailable may set its own with `Tries`, and its own waits between attempts with `Backoff`: the same `queue.Tries` and `queue.Backoff` interfaces a [job](queues.md#job-structure) implements, so a mail's backoff is durations, as a job's is. Both are copied onto its job when it is queued. For example, a booking confirmation that must reach the customer might retry patiently rather than burning every attempt inside one outage:

```go
// Tries is the number of delivery attempts before the job fails.
func (BookingConfirmationMail) Tries() int { return 5 }

// Backoff is the wait before each retry.
func (BookingConfirmationMail) Backoff() []time.Duration {
	return []time.Duration{30 * time.Second, 2 * time.Minute, 10 * time.Minute, 30 * time.Minute}
}
```

## Rendering Mailables

Sometimes you may wish to capture the HTML of a mailable without sending it. `Mailer.Render` builds the `mail.Message` a transport would receive (its `From`, `To`, `Subject`, `HTML` and the `Mailable` it was rendered from) without sending it:

```go
message, err := a.Mail.Render(ctx, appmail.ResetPasswordMail{URL: link}, "ana@example.com")
if err != nil {
	return err
}

fmt.Println(message.Subject, len(message.HTML))
```

A rendering error comes back as `mail: rendering <type>: ...`.

## Testing Mailables

In feature tests, build the mailer on a `*mail.Fake` transport (`tc.Mail` below, a field of your own [test case](testing.md#a-full-test-case)), which records every message instead of sending it; `Fake.Sent()` returns them in order. Assert on them with the `mailtest` package, by mailable type, and check recipients with `Message.HasTo`, which compares addresses case-insensitively:

```go
mailtest.AssertSentTimes[appmail.WelcomeMail](t, tc.Mail, 1)
mailtest.AssertSent(t, tc.Mail, func(_ appmail.WelcomeMail, m mail.Message) bool {
	return m.HasTo("ana@example.com") && strings.Contains(m.HTML, "Ana Welcome")
})
```

To assert that mail was queued rather than sent, fake the queue (`tc.FakeQueue()`) and read its `mail.SendQueuedMailable` jobs with `queuetest.Pushed`; each names its mailable by kind in its `Mailable` field, as in [the example above](#queued-mail-and-secrets). `Mailer.HandleQueued(ctx, job)` then sends one the way the worker would: unseal, rebuild, hydrate, render, send:

```go
job := queuetest.Pushed[mail.SendQueuedMailable](pushed)[0]
if err := tc.App.Mail.HandleQueued(tc.Ctx, job); err != nil {
	t.Fatal(err)
}

mailtest.AssertSentTimes[appmail.ResetPasswordMail](t, tc.Mail, 1)
```

See [mocking](mocking.md) for every `mailtest` assertion.

## Mail and Local Development

When you are developing an application that sends email, you probably don't want to actually send emails to live email addresses. `mail.LogTransport` writes each message to the log instead, as a `mail (log transport)` line with its recipients, its subject and the size of its HTML. The body itself isn't logged; render the mailable to look at it.

## Custom Transports

reef's transports may not cover your provider. To send through another, implement `mail.Transport`, whose only method receives the rendered message. Here is a transport for Postmark's API, sending a typed request through the [HTTP client](http-client.md):

```go
// SendPostmarkEmail is POST /email.
type SendPostmarkEmail struct {
	From     string `json:"From"`
	To       string `json:"To"`
	Subject  string `json:"Subject"`
	HTMLBody string `json:"HtmlBody"`
}

var _ httpclient.WithBody = SendPostmarkEmail{}

func (SendPostmarkEmail) Method() string   { return "POST" }
func (SendPostmarkEmail) Endpoint() string { return "/email" }
func (r SendPostmarkEmail) Body() any      { return r }

// PostmarkTransport sends through Postmark's API.
type PostmarkTransport struct {
	Client *httpclient.Client
}

// NewPostmarkTransport authenticates with a server token.
func NewPostmarkTransport(token string) PostmarkTransport {
	return PostmarkTransport{Client: httpclient.New(httpclient.Connector{
		BaseURL: "https://api.postmarkapp.com",
		Headers: map[string]string{"X-Postmark-Server-Token": token},
		Timeout: 30 * time.Second,
	})}
}

// Send implements mail.Transport.
func (t PostmarkTransport) Send(ctx context.Context, message mail.Message) error {
	res, err := t.Client.Send(ctx, SendPostmarkEmail{
		From:     message.From,
		To:       strings.Join(message.To, ","),
		Subject:  message.Subject,
		HTMLBody: message.HTML,
	})
	if err != nil {
		return fmt.Errorf("mail: postmark: %w", err)
	}

	return res.Err()
}
```

Then hand it to the mailer as its `Transport`.

## Available Methods

#### Package

- `mail.NewMailer(config)`: the mailer, registering its queued-mail handler on `config.Workers`.
- `mail.NewBentoTransport(config)`: the Bento transport.
- `mail.ErrNoQueue`: returned when a mailable is queued on a mailer without a queue.

#### Mailer and Pending

- `Mailer.To(addresses...)`: addresses the next mailable, returning a `*Pending`.
- `Pending.Send(ctx, mailable)`: renders and delivers now, or queues a `ShouldQueue` mailable.
- `Pending.Queue(ctx, mailable)`: pushes a `ShouldQueue` mailable onto the queue.
- `Mailer.Render(ctx, mailable, to...)`: builds the message without sending it.
- `Mailer.Open(job)`: the job with its sealed payload and recipients decrypted.
- `Mailer.HandleQueued(ctx, job)`: the queued-mail handler: unseal, rebuild, hydrate, render, send.

#### Mailables

- `Mailable`: `Envelope()` and `Content(ctx, views)`.
- `Envelope`: `Subject`, and `From` to override the default sender.
- `ShouldQueue`: a mailable with a `MailKind() string` method, its name in the queue table, is queued by `Send`. A mailable may also implement `queue.Tries` and `queue.Backoff`, copied onto its job.
- `Hydrator`: `Hydrate(ctx)`, called by the worker after rebuilding a queued mailable.
- `Renderer`: `Render(ctx, name, data)`, the templates' interface.
- `Encrypter`: `EncryptString` and `DecryptString`, what seals queued mail.

#### Messages and Transports

- `Message`: `From`, `To`, `Subject`, `HTML` and `Mailable`; `HasTo(address)` checks the recipients, case-insensitively.
- `Transport`: `Send(ctx, message)`.
- `LogTransport.Send`: logs the message.
- `Fake.Send`: records the message. `Fake.Sent()`: everything sent, in order.
- `BentoTransport.Send`: delivers through Bento.
- `BentoConfig`: `PublishableKey`, `SecretKey`, `SiteUUID`, `Mock`.
- `SendBentoEmails`: the Bento request, with `SiteUUID` and `Email` fields: `Method()` is `POST`, `Endpoint()` is `/api/v1/batch/emails`, `Query()` carries `site_uuid`, and `Body()` wraps the email in an `emails` batch.

#### The Queued Job

- `SendQueuedMailable`: the job a queued mailable travels in. Its fields are `Mailable` (the mailable's kind), `Payload` (its JSON), `To`, `Sealed` (the encrypted payload and recipients), `MaxTries` and `Delays`.
- `SendQueuedMailable.Kind()`: `mail_send_queued_mailable`.
- `SendQueuedMailable.Tries()`: the mailable's own attempts; zero falls back to the queue's default.
- `SendQueuedMailable.Backoff()`: the mailable's own backoff as durations; empty means the queue's default.
