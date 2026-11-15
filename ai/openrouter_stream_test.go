package ai_test

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/atobeach/reef/ai"
	"github.com/atobeach/reef/ai/aitest"
	"github.com/atobeach/reef/httpclient"
)

// TestStreamsTokensAsTheProviderSendsThem runs the gateway against a real
// (local) HTTP server: the first delta must reach the consumer while the
// provider is still holding the rest of the stream, so tokens are never
// buffered behind the whole response.
func TestStreamsTokensAsTheProviderSendsThem(t *testing.T) {
	firstSeen := make(chan struct{})
	var received map[string]any

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewDecoder(r.Body).Decode(&received)
		if r.Header.Get("Authorization") != "Bearer test-key" || r.URL.Path != "/api/v1/chat/completions" {
			http.Error(w, "bad request", http.StatusBadRequest)

			return
		}

		w.Header().Set("Content-Type", "text/event-stream")
		body := string(aitest.SSE(aitest.Text("Hello", " world")))
		frames := strings.SplitAfter(body, "\n\n")

		_, _ = w.Write([]byte(frames[0]))
		_ = http.NewResponseController(w).Flush()

		select {
		case <-firstSeen:
		case <-time.After(5 * time.Second):
			t.Error("the first delta was not delivered while the stream was open")
		}

		for _, frame := range frames[1:] {
			_, _ = w.Write([]byte(frame))
		}
	}))
	defer server.Close()

	manager := ai.NewManager(ai.ManagerConfig{
		Providers: map[string]ai.ProviderConfig{"openrouter": {Name: "openrouter", Driver: "openrouter", Key: "test-key", URL: server.URL + "/api/v1"}},
		Gateway:   ai.OpenRouterGateway{HTTP: httpclient.New},
	})

	streamable, err := manager.Stream(context.Background(), testAgent{}, "Hi")
	if err != nil {
		t.Fatal(err)
	}

	var text strings.Builder
	once := false
	err = streamable.Each(context.Background(), func(event ai.StreamEvent) {
		if delta, ok := event.(ai.TextDelta); ok {
			text.WriteString(delta.Delta)
			if !once {
				once = true
				close(firstSeen)
			}
		}
	})
	if err != nil {
		t.Fatal(err)
	}

	if text.String() != "Hello world" || received["stream"] != true {
		t.Fatalf("text %q, body %v", text.String(), received)
	}
}
