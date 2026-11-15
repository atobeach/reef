package mail_test

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/atobeach/reef/crypt"
	"github.com/atobeach/reef/mail"
	"github.com/atobeach/reef/mail/mailtest"
	"github.com/atobeach/reef/queue"
	"github.com/atobeach/reef/queue/queuetest"
)

type confirmation struct {
	Reference string `json:"reference"`
}

func (confirmation) Envelope() mail.Envelope { return mail.Envelope{Subject: "Booking confirmed"} }
func (c confirmation) Content(context.Context, mail.Renderer) (string, error) {
	return "<p>" + c.Reference + "</p>", nil
}
func (confirmation) MailKind() string { return "confirmation" }

// renamed is confirmation after a rename: another Go type, the same kind.
type renamed struct {
	Reference string `json:"reference"`
}

func (renamed) Envelope() mail.Envelope { return mail.Envelope{Subject: "Booking confirmed"} }
func (r renamed) Content(context.Context, mail.Renderer) (string, error) {
	return "<p>" + r.Reference + "</p>", nil
}
func (renamed) MailKind() string { return "confirmation" }

// unnamed has no kind.
type unnamed struct{ confirmation }

func (unnamed) MailKind() string { return "" }

// patient retries on its own schedule.
type patient struct{ confirmation }

func (patient) MailKind() string { return "patient" }
func (patient) Tries() int       { return 5 }
func (patient) Backoff() []time.Duration {
	return []time.Duration{30 * time.Second, 2 * time.Minute}
}

func TestShouldQueueMailablesArePushedThenSentByTheWorker(t *testing.T) {
	sent := &mail.Fake{}
	pushed := &queue.Fake{}
	mailer := mail.NewMailer(mail.MailerConfig{Transport: sent, From: "hello@example.com", Queue: pushed, Queueable: []mail.ShouldQueue{confirmation{}}})

	if err := mailer.To("ana@example.com").Send(context.Background(), confirmation{Reference: "QX7P2M"}); err != nil {
		t.Fatal(err)
	}

	// A ShouldQueue mailable is never sent inline.
	mailtest.AssertNothingSent(t, sent)
	queuetest.AssertCount(t, pushed, 1)
	queuetest.AssertPushed(t, pushed, func(job mail.SendQueuedMailable) bool { return slices.Equal(job.To, []string{"ana@example.com"}) })

	// The worker side: rebuild from JSON, render, send.
	if err := mailer.HandleQueued(context.Background(), queuetest.Pushed[mail.SendQueuedMailable](pushed)[0]); err != nil {
		t.Fatal(err)
	}

	mailtest.AssertSentTimes[confirmation](t, sent, 1)
	mailtest.AssertSent(t, sent, func(_ confirmation, m mail.Message) bool {
		return m.HasTo("ana@example.com") && m.HTML == "<p>QX7P2M</p>"
	})
}

func TestQueuedMailRunsThroughTheSyncConnection(t *testing.T) {
	sent := &mail.Fake{}
	workers := queue.NewWorkers(nil)
	mailer := mail.NewMailer(mail.MailerConfig{
		Transport: sent, From: "hello@example.com",
		Queue: queue.Sync{Workers: workers}, Workers: workers, Queueable: []mail.ShouldQueue{confirmation{}},
	})

	if err := mailer.To("ana@example.com").Queue(context.Background(), confirmation{Reference: "R8VN3T"}); err != nil {
		t.Fatal(err)
	}

	// The sync connection sends at once.
	mailtest.AssertSentTimes[confirmation](t, sent, 1)
	mailtest.AssertSentTo[confirmation](t, sent, "ana@example.com")
}

func TestQueueingNeedsAQueueableTypeAndAQueue(t *testing.T) {
	unqueued := mail.NewMailer(mail.MailerConfig{Transport: &mail.Fake{}, From: "hello@example.com", Queueable: []mail.ShouldQueue{confirmation{}}})
	if err := unqueued.To("a@b.co").Queue(context.Background(), confirmation{}); !errors.Is(err, mail.ErrNoQueue) {
		t.Fatalf("err = %v", err)
	}

	unlisted := mail.NewMailer(mail.MailerConfig{Transport: &mail.Fake{}, From: "hello@example.com", Queue: &queue.Fake{}})
	if err := unlisted.To("a@b.co").Queue(context.Background(), confirmation{}); err == nil {
		t.Fatal("a mailable the worker cannot rebuild must not be queued")
	}

	// Without a queue, Send on a ShouldQueue mailable delivers inline.
	inline := &mail.Fake{}
	if err := mail.NewMailer(mail.MailerConfig{Transport: inline, From: "x@y.z"}).To("a@b.co").Send(context.Background(), confirmation{}); err != nil {
		t.Fatalf("inline send: %v", err)
	}

	mailtest.AssertSentTimes[confirmation](t, inline, 1)
}

func TestAnEncryptingMailerSealsTheJobArgsAndStillOpensUnsealedJobs(t *testing.T) {
	encrypter, err := crypt.New("base64:" + base64.StdEncoding.EncodeToString([]byte("0123456789abcdef0123456789abcdef")))
	if err != nil {
		t.Fatal(err)
	}

	sent := &mail.Fake{}
	pushed := &queue.Fake{}
	mailer := mail.NewMailer(mail.MailerConfig{
		Transport: sent, From: "hello@example.com",
		Queue: pushed, Queueable: []mail.ShouldQueue{confirmation{}}, Encrypter: encrypter,
	})

	if err := mailer.To("ana@example.com").Queue(context.Background(), confirmation{Reference: "S3CR3T"}); err != nil {
		t.Fatal(err)
	}

	job := queuetest.Pushed[mail.SendQueuedMailable](pushed)[0]
	stored, _ := json.Marshal(job)
	if job.Sealed == "" || strings.Contains(string(stored), "S3CR3T") || strings.Contains(string(stored), "ana@example.com") {
		t.Fatalf("stored args: %s", stored)
	}

	if err := mailer.HandleQueued(context.Background(), job); err != nil {
		t.Fatal(err)
	}
	mailtest.AssertSentTimes[confirmation](t, sent, 1)
	mailtest.AssertSentTo[confirmation](t, sent, "ana@example.com")

	// A job queued before sealing was switched on still sends.
	legacy := mail.SendQueuedMailable{Mailable: job.Mailable, Payload: json.RawMessage(`{"reference":"K4DL9Z"}`), To: []string{"old@example.com"}}
	if err := mailer.HandleQueued(context.Background(), legacy); err != nil {
		t.Fatalf("legacy job: %v", err)
	}
	mailtest.AssertSentTimes[confirmation](t, sent, 2)
	mailtest.AssertSentTo[confirmation](t, sent, "old@example.com")

	// A sealed job cannot be sent by a mailer without the key.
	keyless := mail.NewMailer(mail.MailerConfig{Transport: &mail.Fake{}, From: "hello@example.com", Queueable: []mail.ShouldQueue{confirmation{}}})
	if err := keyless.HandleQueued(context.Background(), job); err == nil {
		t.Fatal("a keyless mailer opened a sealed job")
	}
}

func TestAQueuedMailableTravelsUnderItsKind(t *testing.T) {
	pushed := &queue.Fake{}
	mailer := mail.NewMailer(mail.MailerConfig{Transport: &mail.Fake{}, From: "hello@example.com", Queue: pushed, Queueable: []mail.ShouldQueue{confirmation{}}})

	if err := mailer.To("ana@example.com").Send(context.Background(), confirmation{Reference: "QX7P2M"}); err != nil {
		t.Fatal(err)
	}

	if got := queuetest.Pushed[mail.SendQueuedMailable](pushed)[0].Mailable; got != "confirmation" {
		t.Fatalf("queued as %q, want its kind", got)
	}
}

func TestARenamedMailableStillSendsMailQueuedUnderItsKind(t *testing.T) {
	sent := &mail.Fake{}
	mailer := mail.NewMailer(mail.MailerConfig{Transport: sent, From: "hello@example.com", Queueable: []mail.ShouldQueue{renamed{}}})

	queued := mail.SendQueuedMailable{Mailable: "confirmation", Payload: json.RawMessage(`{"reference":"H2JW6C"}`), To: []string{"ana@example.com"}}
	if err := mailer.HandleQueued(context.Background(), queued); err != nil {
		t.Fatal(err)
	}

	mailtest.AssertSent(t, sent, func(_ renamed, m mail.Message) bool { return m.HTML == "<p>H2JW6C</p>" })
}

func TestQueueableKindsAreNamedAndUnique(t *testing.T) {
	for name, queueable := range map[string][]mail.ShouldQueue{
		"a kind given twice": {confirmation{}, renamed{}},
		"an empty kind":      {unnamed{}},
	} {
		t.Run(name, func(t *testing.T) {
			defer func() {
				if recover() == nil {
					t.Fatal("NewMailer accepted the kinds")
				}
			}()

			mail.NewMailer(mail.MailerConfig{Transport: &mail.Fake{}, Queueable: queueable})
		})
	}
}

func TestAMailableWhoseKindBelongsToAnotherTypeIsNotQueued(t *testing.T) {
	pushed := &queue.Fake{}
	mailer := mail.NewMailer(mail.MailerConfig{Transport: &mail.Fake{}, From: "hello@example.com", Queue: pushed, Queueable: []mail.ShouldQueue{confirmation{}}})

	if err := mailer.To("ana@example.com").Send(context.Background(), renamed{Reference: "QX7P2M"}); err == nil {
		t.Fatal("renamed queued under confirmation's kind")
	}

	queuetest.AssertNothingPushed(t, pushed)
}

func TestAQueuedMailableRetriesWithItsOwnTriesAndBackoff(t *testing.T) {
	pushed := &queue.Fake{}
	mailer := mail.NewMailer(mail.MailerConfig{Transport: &mail.Fake{}, From: "hello@example.com", Queue: pushed, Queueable: []mail.ShouldQueue{patient{}}})

	if err := mailer.To("ana@example.com").Send(context.Background(), patient{}); err != nil {
		t.Fatal(err)
	}

	job := queuetest.Pushed[mail.SendQueuedMailable](pushed)[0]
	if tries := queue.TriesOf(job, 3); tries != 5 {
		t.Fatalf("tries = %d, want the mailable's 5", tries)
	}
	if delay, ok := queue.BackoffFor(job, 1); delay != 30*time.Second || !ok {
		t.Fatalf("first retry after %v, %v; want 30s", delay, ok)
	}
	if delay, _ := queue.BackoffFor(job, 9); delay != 2*time.Minute {
		t.Fatalf("ninth retry after %v, want the last delay repeated", delay)
	}

	stored, err := json.Marshal(job)
	if err != nil {
		t.Fatal(err)
	}
	var decoded mail.SendQueuedMailable
	if err := json.Unmarshal(stored, &decoded); err != nil || !slices.Equal(decoded.Backoff(), job.Backoff()) {
		t.Fatalf("backoff %v after the queue table's round trip, want %v (%v)", decoded.Backoff(), job.Backoff(), err)
	}
}
