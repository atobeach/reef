package mailtest_test

import (
	"context"
	"testing"

	"github.com/atobeach/reef/mail"
	"github.com/atobeach/reef/mail/mailtest"
	"github.com/atobeach/reef/testkit"
)

type welcome struct{ Name string }

func (welcome) Envelope() mail.Envelope { return mail.Envelope{Subject: "Welcome"} }
func (w welcome) Content(context.Context, mail.Renderer) (string, error) {
	return "<p>Hi " + w.Name + "</p>", nil
}

type receipt struct{}

func (receipt) Envelope() mail.Envelope                                { return mail.Envelope{Subject: "Your receipt"} }
func (receipt) Content(context.Context, mail.Renderer) (string, error) { return "<p>Paid</p>", nil }

// sent is a fake that sent Ana a welcome and Ben a welcome and a receipt.
func sent(t *testing.T) *mail.Fake {
	t.Helper()

	fake := &mail.Fake{}
	mailer := mail.NewMailer(mail.MailerConfig{Transport: fake, From: "hello@example.com"})

	for _, send := range []struct {
		to       string
		mailable mail.Mailable
	}{{"ana@example.com", welcome{"Ana"}}, {"ben@example.com", welcome{"Ben"}}, {"ben@example.com", receipt{}}} {
		if err := mailer.To(send.to).Send(context.Background(), send.mailable); err != nil {
			t.Fatal(err)
		}
	}

	return fake
}

func TestAssertionsMatchByMailableTypeAndFailListingWhatWentOut(t *testing.T) {
	cases := []struct {
		name   string
		assert func(t *testkit.RecordingT, f *mail.Fake)
		// failure is "" when the assertion passes, else what it must say.
		failure string
	}{
		{"sent to", func(t *testkit.RecordingT, f *mail.Fake) {
			mailtest.AssertSent(t, f, func(_ welcome, m mail.Message) bool { return m.HasTo("BEN@example.com") && m.HTML == "<p>Hi Ben</p>" })
		}, ""},
		{"by the mailable", func(t *testkit.RecordingT, f *mail.Fake) {
			mailtest.AssertSent(t, f, func(w welcome, m mail.Message) bool { return w.Name == "Ana" && m.HasTo("ana@example.com") })
		}, ""},
		{"a mailable that was not sent", func(t *testkit.RecordingT, f *mail.Fake) {
			mailtest.AssertSent(t, f, func(w welcome, _ mail.Message) bool { return w.Name == "Cleo" })
		}, "no mailtest_test.welcome sent matching the callback"},
		{"sent to someone else", func(t *testkit.RecordingT, f *mail.Fake) {
			mailtest.AssertSent(t, f, func(_ receipt, m mail.Message) bool { return m.HasTo("ana@example.com") })
		}, "no mailtest_test.receipt sent matching the callback; sent: mailtest_test.welcome to [ana@example.com], mailtest_test.welcome to [ben@example.com], mailtest_test.receipt to [ben@example.com]"},
		{"sent to an address", func(t *testkit.RecordingT, f *mail.Fake) { mailtest.AssertSentTo[receipt](t, f, "Ben@Example.com") }, ""},
		{"not sent to that address", func(t *testkit.RecordingT, f *mail.Fake) { mailtest.AssertSentTo[receipt](t, f, "ana@example.com") }, "no mailtest_test.receipt sent to ana@example.com"},
		{"times", func(t *testkit.RecordingT, f *mail.Fake) { mailtest.AssertSentTimes[welcome](t, f, 2) }, ""},
		{"times off", func(t *testkit.RecordingT, f *mail.Fake) { mailtest.AssertSentTimes[receipt](t, f, 2) }, "mailtest_test.receipt sent 1 times, want 2"},
		{"count", func(t *testkit.RecordingT, f *mail.Fake) { mailtest.AssertSentCount(t, f, 3) }, ""},
		{"count off", func(t *testkit.RecordingT, f *mail.Fake) { mailtest.AssertSentCount(t, f, 1) }, "3 sent, want 1"},
		{"nothing sent", func(t *testkit.RecordingT, f *mail.Fake) { mailtest.AssertNothingSent(t, f) }, "3 sent, want none"},
	}

	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			recording := testkit.Record(t)
			c.assert(recording, sent(t))

			if c.failure == "" {
				recording.AssertPassed()
			} else {
				recording.AssertFailed(c.failure)
			}
		})
	}
}

func TestSentIsTheMessagesOfOneMailableType(t *testing.T) {
	welcomes := mailtest.Sent[welcome](sent(t))

	if len(welcomes) != 2 || welcomes[0].Mailable != (welcome{"Ana"}) || welcomes[1].Subject != "Welcome" {
		t.Fatalf("welcomes = %+v", welcomes)
	}
}

func TestAssertNothingSentPassesWhenNothingWentOut(t *testing.T) {
	recording := testkit.Record(t)
	mailtest.AssertNothingSent(recording, &mail.Fake{})

	recording.AssertPassed()
}
