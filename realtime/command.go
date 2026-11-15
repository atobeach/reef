package realtime

import (
	"context"
	"errors"
	"log/slog"
	"net"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/atobeach/reef/console"
)

// StartCommand is `reverb:start`: serve until SIGINT/SIGTERM, then close
// every connection with 1001 so clients reconnect elsewhere.
func StartCommand(cfg Config) console.Command {
	signature := "reverb:start" +
		" {--host= : The IP address the server should bind to (default REVERB_SERVER_HOST or 127.0.0.1)}" +
		" {--port= : The port the server should listen on (default REVERB_SERVER_PORT or 8080)}" +
		" {--path= : The path the server should prefix to all routes}"

	return console.Func(signature, "Start the Reverb server", func(c *console.Context) error {
		if host := c.Option("host"); host != "" {
			cfg.Host = host
		}

		if c.Option("port") != "" {
			port, err := c.IntOption("port", 1)
			if err != nil {
				return err
			}

			cfg.Port = port
		}

		if path := c.Option("path"); path != "" {
			cfg.Path = path
		}

		if cfg.App.Key == "" || cfg.App.Secret == "" || cfg.App.ID == "" {
			return c.Failf("REVERB_APP_ID, REVERB_APP_KEY and REVERB_APP_SECRET must be set.")
		}

		cfg = cfg.withDefaults()

		var lc net.ListenConfig

		listener, err := lc.Listen(c, "tcp", Address(cfg.Host, cfg.Port))
		if err != nil {
			return err
		}

		c.Infof("Starting server on %s%s", listener.Addr(), cfg.Path)

		return Serve(c, listener, cfg, nil)
	})
}

// Address joins host and port, accepting a bracketed IPv6 host such as `[::]`.
func Address(host string, port int) string {
	return net.JoinHostPort(strings.Trim(host, "[]"), strconv.Itoa(port))
}

// Serve runs a server on listener until ctx ends, then drains: the HTTP
// side stops accepting, every WebSocket gets what is queued for it, then
// 1001.
func Serve(ctx context.Context, listener net.Listener, cfg Config, fanout Fanout) error {
	server := NewServer(cfg, fanout)

	httpServer := &http.Server{
		Handler:           server,
		ReadHeaderTimeout: 10 * time.Second,
	}

	runCtx, stop := context.WithCancel(ctx)
	defer stop()

	go func() { _ = server.Run(runCtx) }()

	failed := make(chan error, 1)
	go func() {
		if err := httpServer.Serve(listener); !errors.Is(err, http.ErrServerClosed) {
			failed <- err
		}
	}()

	select {
	case err := <-failed:
		return err
	case <-ctx.Done():
	}

	slog.InfoContext(ctx, "realtime: shutting down", "connections", server.Connections())

	// ctx is already done; the drain still gets its own grace period.
	shutdown, cancel := context.WithTimeout(context.WithoutCancel(ctx), 10*time.Second)
	defer cancel()

	// Shutdown doesn't track hijacked WebSockets; Close does.
	err := httpServer.Shutdown(shutdown)
	server.Close(10 * time.Second)

	return err
}
