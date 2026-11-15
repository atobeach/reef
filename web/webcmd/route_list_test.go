package webcmd

import (
	"strings"
	"testing"

	"github.com/atobeach/reef/console"
	"github.com/atobeach/reef/testkit"
	"github.com/atobeach/reef/web"
)

// route:list sorts by path, pads the methods to one column (a GET answers
// HEAD too, listed right after it) and dots each route out to its name.
func TestRouteListShowsEveryRouteSortedByPath(t *testing.T) {
	t.Setenv("COLUMNS", "60")

	handler := func(*web.Context) (any, error) { return nil, nil }
	router := web.NewRouter()
	router.Prefix("/api").Group(func(r *web.Router) {
		r.Post("/login", handler).Name("login.store")
		r.Get("/airports", handler).Name("airports.index")
		r.Delete("/airports/{code}", handler)
		r.Match([]string{"PUT", "PATCH"}, "/airports/{code}", handler).Name("airports.update")
	})
	router.Match([]string{"GET", "POST"}, "/broadcasting/auth", handler).Name("broadcasting.auth")

	kernel := console.NewKernel(nil, console.KernelConfig{Name: "test"})
	kernel.Register(RouteListCommand(router))

	want := []string{
		"  GET|HEAD      /api/airports " + strings.Repeat(".", 13) + " airports.index  ",
		"  DELETE        /api/airports/{code} " + strings.Repeat(".", 20) + "   ",
		"  PUT|PATCH     /api/airports/{code} " + strings.Repeat(".", 5) + " airports.update  ",
		"  POST          /api/login " + strings.Repeat(".", 19) + " login.store  ",
		"  GET|HEAD|POST /broadcasting/auth " + strings.Repeat(".", 5) + " broadcasting.auth  ",
		"  Showing [5] routes",
	}

	result := testkit.RunCommand(t, t.Context(), kernel, "route:list").AssertSuccessful()

	previous := -1
	for _, line := range want {
		result.ExpectsOutput(line)

		at := strings.Index(result.Output, line)
		if at < previous {
			t.Errorf("%q is out of order:\n%s", line, result.Output)
		}

		previous = at
	}
}
