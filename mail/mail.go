// Package mail renders Mailables to HTML and hands them to a transport:
// Bento, the log, or an in-memory fake in tests.
//
//	type WelcomeMail struct{ User models.User }
//
//	func (m WelcomeMail) Envelope() mail.Envelope { return mail.Envelope{Subject: "Welcome aboard"} }
//	func (m WelcomeMail) Content(ctx context.Context, v mail.Renderer) (string, error) {
//		return v.Render(ctx, "emails/welcome", m)
//	}
//
//	err := mailer.To(user.Email).Send(ctx, WelcomeMail{User: user})
package mail

import (
	"context"
	"fmt"
	"log/slog"
	"strings"
	"sync"

	"github.com/atobeach/reef/queue"
)

// Envelope is a Mailable's addressing.
type Envelope struct {
	Subject string
	// From overrides the mailer's default sender.
	From string
}

// Mailable is one email type.
type Mailable interface {
	Envelope() Envelope
	// Content renders the HTML body with the mailer's templates.
	Content(ctx context.Context, views Renderer) (string, error)
}

// Renderer renders a named template (resources/views) with data; ctx's
// clock dates the render.
type Renderer interface {
	Render(ctx context.Context, name string, data any) (string, error)
}

// Message is a rendered email, what transports send.
type Message struct {
	From    string
	To      []string
	Subject string
	HTML    string
	// Mailable is what the message was rendered from, so a fake can be
	// asked which mailables went out.
	Mailable Mailable
}

// HasTo reports whether address is among the recipients (case-insensitive).
func (m Message) HasTo(address string) bool {
	for _, to := range m.To {
		if strings.EqualFold(to, address) {
			return true
		}
	}

	return false
}

// Transport delivers a rendered message.
type Transport interface {
	Send(ctx context.Context, message Message) error
}

// MailerConfig configures the mailer.
type MailerConfig struct {
	Transport Transport
	// From is the default sender (MAIL_FROM_ADDRESS).
	From string
	// Views are the templates mailables render with.
	Views Renderer
	// Queue receives ShouldQueue mailables; without one they send inline.
	Queue queue.Dispatcher
	// Workers, when set, gets the handler that sends queued mailables.
	Workers *queue.Workers
	// Queueable lists one value per mailable type the worker may rebuild
	// from JSON by its MailKind, so their fields must be exported and
	// JSON-serialisable. An empty or repeated kind panics.
	Queueable []ShouldQueue
	// Encrypter seals queued mailables (see SendQueuedMailable).
	Encrypter Encrypter
}

// Mailer renders mailables and sends them through its transport.
type Mailer struct {
	config MailerConfig
	// kinds rebuild the Queueable mailables by MailKind.
	kinds map[string]queueable
}

// NewMailer builds the mailer, registering the queued-mail handler on
// config.Workers.
func NewMailer(config MailerConfig) *Mailer {
	m := &Mailer{config: config, kinds: queueables(config.Queueable)}
	if config.Workers != nil {
		queue.Register(config.Workers, m.HandleQueued)
	}

	return m
}

// Pending is an addressed send, awaiting the mailable.
type Pending struct {
	mailer *Mailer
	to     []string
}

// To addresses the next mailable.
func (m *Mailer) To(addresses ...string) *Pending {
	return &Pending{mailer: m, to: addresses}
}

// Send renders and delivers now, or queues a ShouldQueue mailable on a
// mailer with a queue.
func (p *Pending) Send(ctx context.Context, mailable Mailable) error {
	if queued, ok := mailable.(ShouldQueue); ok && p.mailer.config.Queue != nil {
		return p.Queue(ctx, queued)
	}

	return p.sendNow(ctx, mailable)
}

func (p *Pending) sendNow(ctx context.Context, mailable Mailable) error {
	message, err := p.mailer.Render(ctx, mailable, p.to...)
	if err != nil {
		return err
	}

	return p.mailer.config.Transport.Send(ctx, message)
}

// Render builds the message without sending it (previews, queued payloads).
func (m *Mailer) Render(ctx context.Context, mailable Mailable, to ...string) (Message, error) {
	envelope := mailable.Envelope()

	html, err := mailable.Content(ctx, m.config.Views)
	if err != nil {
		return Message{}, fmt.Errorf("mail: rendering %T: %w", mailable, err)
	}

	from := envelope.From
	if from == "" {
		from = m.config.From
	}

	return Message{From: from, To: to, Subject: envelope.Subject, HTML: html, Mailable: mailable}, nil
}

// LogTransport writes messages to the log (MAIL_MAILER=log).
type LogTransport struct{}

// Send logs the message.
func (LogTransport) Send(ctx context.Context, message Message) error {
	slog.InfoContext(ctx, "mail (log transport)", "to", strings.Join(message.To, ","), "subject", message.Subject, "bytes", len(message.HTML))

	return nil
}

// Fake records messages for test assertions (mailtest).
type Fake struct {
	mu   sync.Mutex
	sent []Message
}

// Send records the message.
func (f *Fake) Send(_ context.Context, message Message) error {
	f.mu.Lock()
	defer f.mu.Unlock()

	f.sent = append(f.sent, message)

	return nil
}

// Sent is everything sent, in order.
func (f *Fake) Sent() []Message {
	f.mu.Lock()
	defer f.mu.Unlock()

	return append([]Message(nil), f.sent...)
}
