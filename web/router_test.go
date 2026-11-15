package web

import (
	"net/http"
	"reflect"
	"testing"
)

// The distinct types the routes below answer.
type (
	listed    []string
	stored    struct{ ID int }
	replaced  map[string]string
	patched   struct{ Name string }
	destroyed bool
	matched   string
)

// Each registration method records its action's own type on the route, so
// what a route answers can be checked without running it.
func TestEachRouteRecordsItsActionsResponseType(t *testing.T) {
	r := NewRouter()
	cases := []struct {
		route *Route
		want  reflect.Type
	}{
		{r.Get("/a", func(*Context) (listed, error) { return listed{}, nil }), reflect.TypeFor[listed]()},
		{r.Post("/a", func(*Context) (stored, error) { return stored{}, nil }), reflect.TypeFor[stored]()},
		{r.Put("/a", func(*Context) (replaced, error) { return replaced{}, nil }), reflect.TypeFor[replaced]()},
		{r.Patch("/a", func(*Context) (patched, error) { return patched{}, nil }), reflect.TypeFor[patched]()},
		{r.Delete("/a", func(*Context) (destroyed, error) { return false, nil }), reflect.TypeFor[destroyed]()},
		{r.Match([]string{http.MethodOptions}, "/a", func(*Context) (matched, error) { return "", nil }), reflect.TypeFor[matched]()},
		{r.Get("/b", func(*Context) (*Response, error) { return NoContent(), nil }), reflect.TypeFor[*Response]()},
	}

	for _, tc := range cases {
		if tc.route.ResponseType != tc.want {
			t.Errorf("%v %s answers %v, want %v", tc.route.Methods, tc.route.Path, tc.route.ResponseType, tc.want)
		}
	}
}
