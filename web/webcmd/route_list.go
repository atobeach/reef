// Package webcmd holds the router's console commands, so the router never
// links the console.
package webcmd

import (
	"cmp"
	"fmt"
	"net/http"
	"slices"
	"strings"

	"github.com/atobeach/reef/console"
	"github.com/atobeach/reef/web"
)

// RouteListCommand is `route:list`: every route sorted by path, one
// "GET|HEAD|POST /path ........ name" line each, the methods padded to one
// column.
func RouteListCommand(router *web.Router) console.Command {
	return console.Func("route:list", "List all registered routes", func(c *console.Context) error {
		routes := router.Routes()
		slices.SortStableFunc(routes, func(a, b *web.Route) int { return cmp.Compare(a.Path, b.Path) })

		methods := make([]string, len(routes))
		width := 0
		for i, route := range routes {
			methods[i] = strings.Join(listedMethods(route), "|")
			width = max(width, len(methods[i]))
		}

		c.NewLine()

		for i, route := range routes {
			c.TwoColumnDetail(fmt.Sprintf("%-*s %s", width, methods[i], route.Path), route.RouteName)
		}

		c.NewLine()
		c.Linef("  Showing [%d] routes", len(routes))

		return nil
	})
}

// listedMethods are the methods a route answers: ServeMux answers HEAD on
// every GET route.
func listedMethods(route *web.Route) []string {
	var methods []string
	for _, method := range route.Methods {
		methods = append(methods, method)
		if method == http.MethodGet {
			methods = append(methods, http.MethodHead)
		}
	}

	return methods
}
