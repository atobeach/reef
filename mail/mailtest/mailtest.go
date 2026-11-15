// Package mailtest asserts on the mail a mail.Fake transport sent, by
// mailable type. A failure lists the mailables that did go out.
//
//	mailtest.AssertSentTo[OrderShipped](t, fake, "ana@example.com")
//	mailtest.AssertSentTimes[OrderShipped](t, fake, 1)
//	mailtest.AssertSent(t, fake, func(shipped OrderShipped, m mail.Message) bool { return shipped.OrderID == 7 && m.HasTo("ana@example.com") })
package mailtest

import (
	"fmt"
	"reflect"
	"testing"

	"github.com/atobeach/reef/mail"
	"github.com/atobeach/reef/testkit"
)

// Sent is every message rendered from a T, in order.
func Sent[T mail.Mailable](f *mail.Fake) []mail.Message {
	var sent []mail.Message
	for _, message := range f.Sent() {
		if _, ok := message.Mailable.(T); ok {
			sent = append(sent, message)
		}
	}

	return sent
}

// AssertSent fails unless a T went out that match accepts, given the
// mailable and the message it rendered (its recipients, subject and HTML).
func AssertSent[T mail.Mailable](t testing.TB, f *mail.Fake, match func(mailable T, m mail.Message) bool) {
	t.Helper()

	for _, message := range f.Sent() {
		if mailable, ok := message.Mailable.(T); ok && match(mailable, message) {
			return
		}
	}

	t.Fatalf("mail: no %s sent matching the callback; sent: %s", reflect.TypeFor[T](), summary(f))
}

// AssertSentTo fails unless a T went out to address (case-insensitive).
func AssertSentTo[T mail.Mailable](t testing.TB, f *mail.Fake, address string) {
	t.Helper()

	for _, message := range Sent[T](f) {
		if message.HasTo(address) {
			return
		}
	}

	t.Fatalf("mail: no %s sent to %s; sent: %s", reflect.TypeFor[T](), address, summary(f))
}

// AssertSentTimes fails unless exactly n T mailables went out.
func AssertSentTimes[T mail.Mailable](t testing.TB, f *mail.Fake, n int) {
	t.Helper()

	if got := len(Sent[T](f)); got != n {
		t.Fatalf("mail: %s sent %d times, want %d; sent: %s", reflect.TypeFor[T](), got, n, summary(f))
	}
}

// AssertSentCount fails unless exactly n mails went out, of any mailable.
func AssertSentCount(t testing.TB, f *mail.Fake, n int) {
	t.Helper()

	if got := len(f.Sent()); got != n {
		t.Fatalf("mail: %d sent, want %d; sent: %s", got, n, summary(f))
	}
}

// AssertNothingSent fails if any mail went out.
func AssertNothingSent(t testing.TB, f *mail.Fake) {
	t.Helper()

	if sent := f.Sent(); len(sent) > 0 {
		t.Fatalf("mail: %d sent, want none; sent: %s", len(sent), summary(f))
	}
}

// summary names the mailables that went out, with their recipients.
func summary(f *mail.Fake) string {
	sent := f.Sent()

	names := make([]string, len(sent))
	for i, message := range sent {
		names[i] = fmt.Sprintf("%T to %v", message.Mailable, message.To)
	}

	return testkit.Summary(names)
}
