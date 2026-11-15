package mail

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"reflect"
	"time"

	"github.com/atobeach/reef/queue"
)

// ShouldQueue is a mailable Send queues as a SendQueuedMailable rather than
// sending inline. MailKind names it in the queued job, so, like a job's Kind,
// it never changes once mail of that kind may be queued.
type ShouldQueue interface {
	Mailable
	MailKind() string
}

// SendQueuedMailable is the job a queued mailable travels in as JSON,
// rendered by the worker so a template fix deployed meanwhile applies. A
// mail's fields are secrets (a reset link, a code, personal details) and
// job args linger in the queue table, so with an encrypter Payload and To
// travel only inside Sealed.
type SendQueuedMailable struct {
	// Mailable is the mailable's MailKind, which the worker rebuilds it by.
	Mailable string          `json:"mailable"`
	Payload  json.RawMessage `json:"payload,omitempty"`
	To       []string        `json:"to,omitempty"`
	// Sealed is the encrypted {payload, to} pair (empty when unencrypted).
	Sealed string `json:"sealed,omitempty"`
	// MaxTries and Delays carry the mailable's own queue.Tries and
	// queue.Backoff, so a mail sent only once retries with backoff instead
	// of burning every attempt in one blip.
	MaxTries int             `json:"tries,omitempty"`
	Delays   []time.Duration `json:"backoff,omitempty"`
}

var (
	_ queue.Tries   = SendQueuedMailable{}
	_ queue.Backoff = SendQueuedMailable{}
)

// Kind implements queue.Job.
func (SendQueuedMailable) Kind() string { return "mail_send_queued_mailable" }

// Tries is the mailable's own attempt limit; zero falls back to the worker's
// default.
func (j SendQueuedMailable) Tries() int { return j.MaxTries }

// Backoff is the mailable's own retry backoff; empty means none.
func (j SendQueuedMailable) Backoff() []time.Duration { return j.Delays }

// sealedMailable is what SendQueuedMailable.Sealed encrypts.
type sealedMailable struct {
	Payload json.RawMessage `json:"payload"`
	To      []string        `json:"to"`
}

// Encrypter seals queued mailables (a crypt.Encrypter).
type Encrypter interface {
	EncryptString(plaintext string) (string, error)
	DecryptString(encoded string) (string, error)
}

// Hydrator is a queued mailable carrying references (a user id), not the
// records it renders, which the worker loads with Hydrate. A nil mailable
// means the record is gone, and nothing is sent.
type Hydrator interface {
	Hydrate(ctx context.Context) (Mailable, error)
}

// queueable is a registered ShouldQueue type, rebuilt from its JSON.
type queueable struct {
	t reflect.Type
}

func (q queueable) decode(raw json.RawMessage) (Mailable, error) {
	rebuilt := reflect.New(q.t)
	if err := json.Unmarshal(raw, rebuilt.Interface()); err != nil {
		return nil, err
	}

	return rebuilt.Elem().Interface().(Mailable), nil
}

// queueables indexes the registered mailables by MailKind. An empty kind, or
// one kind given twice, would send another type's mail from a payload, so it
// panics.
func queueables(registered []ShouldQueue) map[string]queueable {
	kinds := make(map[string]queueable, len(registered))
	for _, mailable := range registered {
		kind, t := mailable.MailKind(), reflect.TypeOf(mailable)
		if kind == "" {
			panic(fmt.Sprintf("mail: %s has an empty MailKind", t))
		}

		if other, taken := kinds[kind]; taken {
			panic(fmt.Sprintf("mail: kind %q is both %s and %s", kind, other.t, t))
		}

		kinds[kind] = queueable{t: t}
	}

	return kinds
}

// ErrNoQueue means a mailable was queued on a mailer with no queue.
var ErrNoQueue = errors.New("mail: no queue connection configured (MailerConfig.Queue)")

// Open is the job with its sealed payload and recipients decrypted (a job
// queued unsealed is returned as it is).
func (m *Mailer) Open(job SendQueuedMailable) (SendQueuedMailable, error) {
	if job.Sealed == "" {
		return job, nil
	}

	if m.config.Encrypter == nil {
		return job, fmt.Errorf("mail: queued %s is sealed but the mailer has no encrypter", job.Mailable)
	}

	plaintext, err := m.config.Encrypter.DecryptString(job.Sealed)
	if err != nil {
		return job, fmt.Errorf("mail: unsealing queued %s: %w", job.Mailable, err)
	}

	var sealed sealedMailable
	if err := json.Unmarshal([]byte(plaintext), &sealed); err != nil {
		return job, fmt.Errorf("mail: unsealing queued %s: %w", job.Mailable, err)
	}

	job.Payload, job.To, job.Sealed = sealed.Payload, sealed.To, ""

	return job, nil
}

func (m *Mailer) seal(job SendQueuedMailable) (SendQueuedMailable, error) {
	if m.config.Encrypter == nil {
		return job, nil
	}

	plaintext, err := json.Marshal(sealedMailable{Payload: job.Payload, To: job.To})
	if err != nil {
		return job, err
	}

	sealed, err := m.config.Encrypter.EncryptString(string(plaintext))
	if err != nil {
		return job, fmt.Errorf("mail: sealing queued %s: %w", job.Mailable, err)
	}

	job.Payload, job.To, job.Sealed = nil, nil, sealed

	return job, nil
}

// Queue pushes the mailable onto the queue.
func (p *Pending) Queue(ctx context.Context, mailable ShouldQueue) error {
	if p.mailer.config.Queue == nil {
		return ErrNoQueue
	}

	// A kind registered for another type would have the worker render that
	// type's email from this payload.
	kind := mailable.MailKind()
	if registered, ok := p.mailer.kinds[kind]; !ok || registered.t != reflect.TypeOf(mailable) {
		return fmt.Errorf("mail: %T (kind %q) is not queueable (MailerConfig.Queueable)", mailable, kind)
	}

	payload, err := json.Marshal(mailable)
	if err != nil {
		return fmt.Errorf("mail: encoding %s: %w", kind, err)
	}

	job := SendQueuedMailable{Mailable: kind, Payload: payload, To: p.to}
	if tries, ok := mailable.(queue.Tries); ok {
		job.MaxTries = tries.Tries()
	}

	if backoff, ok := mailable.(queue.Backoff); ok {
		job.Delays = backoff.Backoff()
	}

	job, err = p.mailer.seal(job)
	if err != nil {
		return err
	}

	return p.mailer.config.Queue.Dispatch(ctx, job)
}

// HandleQueued sends a SendQueuedMailable: unseal, rebuild, hydrate, render,
// send.
func (m *Mailer) HandleQueued(ctx context.Context, job SendQueuedMailable) error {
	job, err := m.Open(job)
	if err != nil {
		return err
	}

	registered, ok := m.kinds[job.Mailable]
	if !ok {
		return fmt.Errorf("mail: queued mailable %s is not queueable", job.Mailable)
	}

	mailable, err := registered.decode(job.Payload)
	if err != nil {
		return fmt.Errorf("mail: decoding queued %s: %w", job.Mailable, err)
	}

	if hydrator, ok := mailable.(Hydrator); ok {
		mailable, err = hydrator.Hydrate(ctx)
		if err != nil {
			return fmt.Errorf("mail: hydrating queued %s: %w", job.Mailable, err)
		}

		if mailable == nil {
			return nil
		}
	}

	return m.To(job.To...).sendNow(ctx, mailable)
}
