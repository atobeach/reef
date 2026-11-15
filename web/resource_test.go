package web

import (
	"slices"
	"strings"
	"testing"
)

// profiles records the action each request reached; each action answers a
// type of its own.
type profiles struct{ reached []string }

// profile is what show answers.
type profile struct {
	ID string `json:"id"`
}

func (p *profiles) record(c *Context, action string) {
	p.reached = append(p.reached, c.Method()+" "+action+" "+c.Param("profile"))
}

func (p *profiles) Index(c *Context) ([]string, error) {
	p.record(c, "index")

	return []string{}, nil
}

func (p *profiles) Store(c *Context) (*Response, error) {
	p.record(c, "store")

	return HTML("stored"), nil
}

func (p *profiles) Show(c *Context) (profile, error) {
	p.record(c, "show")

	return profile{ID: c.Param("profile")}, nil
}

func (p *profiles) Update(c *Context) (map[string]string, error) {
	p.record(c, "update")

	return map[string]string{"id": c.Param("profile")}, nil
}

func (p *profiles) Destroy(c *Context) (string, error) {
	p.record(c, "destroy")

	return "deleted", nil
}

func TestAPIResourceRegistersTheFiveActions(t *testing.T) {
	controller := &profiles{}
	router := NewRouter()
	router.Prefix("/api").Group(func(r *Router) {
		r.APIResource("/profiles/{profile}", controller)
	})

	var listed []string
	for _, route := range router.Routes() {
		listed = append(listed, strings.Join(route.Methods, "|")+" "+route.Path+" "+route.RouteName+" "+route.ResponseType.String())
	}

	want := []string{
		"GET /api/profiles profiles.index []string",
		"POST /api/profiles profiles.store *web.Response",
		"GET /api/profiles/{profile} profiles.show web.profile",
		"PUT|PATCH /api/profiles/{profile} profiles.update map[string]string",
		"DELETE /api/profiles/{profile} profiles.destroy string",
	}
	if !slices.Equal(listed, want) {
		t.Fatalf("routes = %q, want %q", listed, want)
	}

	for _, request := range [][2]string{
		{"GET", "/api/profiles"},
		{"POST", "/api/profiles"},
		{"GET", "/api/profiles/7"},
		{"PUT", "/api/profiles/7"},
		{"PATCH", "/api/profiles/7"},
		{"DELETE", "/api/profiles/7"},
	} {
		if res := serve(t, router, nil, request[0], request[1]); res.Status() != 200 {
			t.Errorf("%s %s = %d", request[0], request[1], res.Status())
		}
	}

	want = []string{"GET index ", "POST store ", "GET show 7", "PUT update 7", "PATCH update 7", "DELETE destroy 7"}
	if !slices.Equal(controller.reached, want) {
		t.Fatalf("reached %q, want %q", controller.reached, want)
	}
}

// A nested resource is named after every static segment of its parent.
func TestAPIResourceNamesANestedResourceAfterItsParents(t *testing.T) {
	router := NewRouter()
	router.APIResource("/countries/{country}/cities/{city}", &profiles{})

	if url, err := router.URL("countries.cities.update", "portugal", "lisbon"); err != nil || url != "/countries/portugal/cities/lisbon" {
		t.Fatalf("url = %q, err = %v", url, err)
	}
}

func TestAPIResourceRefusesAMemberPathWithoutItsParameter(t *testing.T) {
	defer func() {
		if recover() == nil {
			t.Fatal("expected a panic")
		}
	}()

	NewRouter().APIResource("/profiles", &profiles{})
}
