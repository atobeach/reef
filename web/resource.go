package web

import (
	"fmt"
	"net/http"
	"path"
	"strings"
)

// ResourceController is an API resource's five actions, each answering its
// own type.
type ResourceController[Index, Store, Show, Update, Destroy any] interface {
	Index(c *Context) (Index, error)
	Store(c *Context) (Store, error)
	Show(c *Context) (Show, error)
	Update(c *Context) (Update, error)
	Destroy(c *Context) (Destroy, error)
}

// APIResource registers an API resource from its member path
// ("/profiles/{profile}"): index and store on the parent, then show, update
// (PUT and PATCH, one route) and destroy on the member, named after the
// parent's static segments: profiles.index … profiles.destroy.
func (r *Router) APIResource[Index, Store, Show, Update, Destroy any](
	member string, controller ResourceController[Index, Store, Show, Update, Destroy],
) {
	parent, param := path.Split(strings.TrimRight(member, "/"))
	if !strings.HasPrefix(param, "{") {
		panic(fmt.Sprintf("web: APIResource %q must end in its {parameter}", member))
	}

	name := resourceName(parent)

	r.Get(parent, controller.Index).Name(name + ".index")
	r.Post(parent, controller.Store).Name(name + ".store")
	r.Get(member, controller.Show).Name(name + ".show")
	r.Match([]string{http.MethodPut, http.MethodPatch}, member, controller.Update).Name(name + ".update")
	r.Delete(member, controller.Destroy).Name(name + ".destroy")
}

// resourceName dots a parent path's static segments:
// /countries/{country}/cities is countries.cities.
func resourceName(parent string) string {
	var names []string
	for segment := range strings.SplitSeq(strings.Trim(parent, "/"), "/") {
		if segment != "" && !strings.HasPrefix(segment, "{") {
			names = append(names, segment)
		}
	}

	return strings.Join(names, ".")
}
