package realtime

import (
	"bytes"
	"context"
	"net"
	"strings"
	"testing"

	"github.com/atobeach/reef/console"
)

// reverb:start listens where --host and --port say, and refuses to start
// without the app's credentials.
func TestStartListensOnTheGivenAddressAndNeedsCredentials(t *testing.T) {
	var lc net.ListenConfig
	taken, err := lc.Listen(t.Context(), "tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = taken.Close() }()

	port := strings.TrimPrefix(taken.Addr().String(), "127.0.0.1:")
	argv := []string{"reverb:start", "--host=127.0.0.1", "--port=" + port, "--path=/ws"}

	for _, tc := range []struct {
		name string
		app  App
		want string
	}{
		{"an address already taken", App{ID: testAppID, Key: testKey, Secret: testSecret}, taken.Addr().String()},
		{"no credentials", App{}, "REVERB_APP_ID, REVERB_APP_KEY and REVERB_APP_SECRET must be set."},
	} {
		kernel := console.NewKernel(nil, console.KernelConfig{Name: "test"})
		kernel.Register(StartCommand(Config{App: tc.app}))

		var out bytes.Buffer
		if code := kernel.Run(context.Background(), argv, &out); code != console.Failure || !strings.Contains(out.String(), tc.want) {
			t.Errorf("%s: %d %q, want a failure saying %q", tc.name, code, out.String(), tc.want)
		}
	}
}
