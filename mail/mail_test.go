package mail_test

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/atobeach/reef/httpclient"
	"github.com/atobeach/reef/mail"
	"github.com/atobeach/reef/mail/mailtest"
)

type welcome struct{ Name string }

func (welcome) Envelope() mail.Envelope { return mail.Envelope{Subject: "Welcome aboard"} }
func (w welcome) Content(context.Context, mail.Renderer) (string, error) {
	return "<p>Hi " + w.Name + "</p>", nil
}

func TestMailablesRenderAndSend(t *testing.T) {
	fake := &mail.Fake{}
	mailer := mail.NewMailer(mail.MailerConfig{Transport: fake, From: "hello@example.com"})

	if err := mailer.To("ana@example.com").Send(context.Background(), welcome{Name: "Ana"}); err != nil {
		t.Fatal(err)
	}

	mailtest.AssertSentTimes[welcome](t, fake, 1)
	mailtest.AssertSent(t, fake, func(w welcome, m mail.Message) bool {
		return m.HasTo("ANA@example.com") && m.From == "hello@example.com" && m.Subject == "Welcome aboard" && m.HTML == "<p>Hi Ana</p>" && w == welcome{Name: "Ana"}
	})
}

func TestBentoGetsTheSDKsPayload(t *testing.T) {
	mock := httpclient.NewMock(t, map[string]any{"SendBentoEmails": httpclient.JSON(200, map[string]any{"results": 1})})
	transport := mail.NewBentoTransport(mail.BentoConfig{PublishableKey: "pub", SecretKey: "secret", SiteUUID: "site-1", Mock: mock})

	err := mail.NewMailer(mail.MailerConfig{Transport: transport, From: "hello@example.com"}).To("ana@example.com").Send(context.Background(), welcome{Name: "Ana"})
	if err != nil {
		t.Fatal(err)
	}

	sent := mock.Sent()[0]
	var body map[string][]map[string]any
	_ = json.Unmarshal(sent.Body, &body)

	email := body["emails"][0]
	if sent.HTTP.URL.Query().Get("site_uuid") != "site-1" || email["to"] != "ana@example.com" || email["html_body"] != "<p>Hi Ana</p>" || email["transactional"] != true {
		t.Fatalf("request = %s %s", sent.HTTP.URL, sent.Body)
	}

	if auth := sent.HTTP.Header.Get("Authorization"); auth != "Basic cHViOnNlY3JldA==" {
		t.Fatalf("Authorization = %q", auth)
	}
}

func TestBentoAuthFailuresAreExplained(t *testing.T) {
	mock := httpclient.NewMock(t, map[string]any{"SendBentoEmails": httpclient.JSON(401, map[string]any{})})
	transport := mail.NewBentoTransport(mail.BentoConfig{PublishableKey: "pub", SecretKey: "bad", SiteUUID: "site-1", Mock: mock})

	if err := transport.Send(context.Background(), mail.Message{To: []string{"a@b.co"}}); err == nil {
		t.Fatal("a 401 must fail the send")
	}
}
