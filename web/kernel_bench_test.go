package web

import (
	"context"
	"net/http"
	"testing"
)

// discardWriter is a reusable ResponseWriter, so the benchmarks count the
// router's allocations, not the recorder's.
type discardWriter struct{ header http.Header }

func (w *discardWriter) Header() http.Header         { return w.header }
func (w *discardWriter) Write(b []byte) (int, error) { return len(b), nil }
func (w *discardWriter) WriteHeader(int)             {}

func benchmarkHandler(b *testing.B, handler http.Handler) {
	b.Helper()
	b.ReportAllocs()

	req, err := http.NewRequestWithContext(context.Background(), http.MethodGet, "/api/countries/portugal", http.NoBody)
	if err != nil {
		b.Fatal(err)
	}

	w := &discardWriter{header: http.Header{}}
	for b.Loop() {
		clear(w.header)
		handler.ServeHTTP(w, req)
	}
}

// BenchmarkServeMux is the floor: the stdlib router alone on the same route.
func BenchmarkServeMux(b *testing.B) {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /api/countries/{slug}", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/html; charset=UTF-8")
		_, _ = w.Write([]byte(r.PathValue("slug")))
	})

	benchmarkHandler(b, mux)
}

func BenchmarkKernel(b *testing.B) {
	router := NewRouter()
	router.Get("/api/countries/{slug}", func(c *Context) (*Response, error) {
		return HTML(c.Param("slug")), nil
	}).Name("countries.show")

	passthrough := func(c *Context, next Handler) (any, error) { return next(c) }

	kernel, err := NewKernel(nil, KernelConfig{Router: router, Middleware: []Middleware{passthrough}})
	if err != nil {
		b.Fatal(err)
	}

	benchmarkHandler(b, kernel)
}
