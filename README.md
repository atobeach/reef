# reef

reef is a web application framework for Go with the shape of Laravel. It provides named routes and middleware, form requests and validation, models generated from your schema, queued jobs on Postgres, events, mail, broadcasting over a Pusher-compatible realtime server, a scheduler, an Artisan-style console, an HTTP client for third-party APIs, AI agents with tools, and a testing layer that gives each parallel test a database of its own.

## Installation

reef requires Go 1.27.3 or later.

```shell
go get github.com/atobeach/reef
```

## Example

```go
package main

import (
	"log"
	"net/http"

	"github.com/atobeach/reef/web"
)

// Flight is what the route answers, sent as JSON.
type Flight struct {
	Number string `json:"number"`
	From   string `json:"from"`
	To     string `json:"to"`
}

func main() {
	r := web.NewRouter()

	r.Get("/flights/{number}", func(c *web.Context) (Flight, error) {
		return Flight{Number: c.Param("number"), From: "LHR", To: "JFK"}, nil
	}).Name("flights.show")

	kernel, err := web.NewKernel(nil, web.KernelConfig{Router: r})
	if err != nil {
		log.Fatal(err)
	}

	log.Fatal(http.ListenAndServe("127.0.0.1:8000", kernel))
}
```

```shell
$ curl http://127.0.0.1:8000/flights/FL123
{"number":"FL123","from":"LHR","to":"JFK"}
```

## Documentation

The documentation lives in [`docs/`](docs/README.md), starting with the [introduction](docs/README.md) and [installation](docs/installation.md), which walks through building a complete application.

## License

reef is open-source software licensed under the [MIT license](LICENSE).
