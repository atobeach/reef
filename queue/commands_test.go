package queue

import (
	"bytes"
	"context"
	"strings"
	"testing"

	"github.com/atobeach/reef/console"
)

// queue:work reads its connection and options before it starts working, so
// a mistake fails the command instead of starting a worker on the wrong
// settings.
func TestQueueWorkRefusesAnUnknownConnectionOrABadOption(t *testing.T) {
	kernel := console.NewKernel(nil, console.KernelConfig{Name: "test"})
	// Never worked: every case fails before Work.
	kernel.Register(Commands(func() *River { return &River{} }, nil)...)

	for _, tc := range []struct {
		argv []string
		want string
	}{
		{[]string{"queue:work", "redis"}, "Queue connection [redis] is not defined."},
		{[]string{"queue:work", "database", "--queue=high,low", "--concurrency=0"}, `The "--concurrency" option must be a whole number of at least 1.`},
		{[]string{"queue:work", "--sleep=-1"}, `The "--sleep" option must be a whole number of seconds.`},
	} {
		var out bytes.Buffer
		if code := kernel.Run(context.Background(), tc.argv, &out); code != console.Failure || !strings.Contains(out.String(), tc.want) {
			t.Errorf("%q = %d %q, want a failure saying %q", tc.argv, code, out.String(), tc.want)
		}
	}
}
