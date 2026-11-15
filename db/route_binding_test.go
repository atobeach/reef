package db

import (
	"context"
	"errors"
	"reflect"
	"testing"
)

// ticket is a model bound by its uuid rather than its id.
type ticket struct {
	ID      int64  `db:"id"`
	UUID    string `db:"uuid"`
	OwnerID int64  `db:"owner_id"`
}

func (ticket) TableName() string { return "tickets" }

func (ticket) Schema() *Schema { return &ticketSchema }

var ticketSchema = Schema{
	RouteKey: "uuid",
	Columns:  map[string]Column{"id": {Type: "int8"}, "uuid": {Type: "uuid"}, "owner_id": {Type: "int8"}},
}

type ticketQuery struct {
	Builder[ticket, *ticketQuery]
}

func tickets(ctx context.Context) *ticketQuery {
	return NewQuery[ticket](ctx, &ticketQuery{})
}

const ticketUUID = "0199f6a4-7b1c-7d2e-8f3a-4b5c6d7e8f90"

// topic is a model bound by a text slug.
type topic struct {
	ID   int64  `db:"id"`
	Slug string `db:"slug"`
}

func (topic) TableName() string { return "topics" }

func (topic) Schema() *Schema { return &topicSchema }

var topicSchema = Schema{
	RouteKey: "slug",
	Columns:  map[string]Column{"id": {Type: "int8"}, "slug": {Type: "varchar"}},
}

type topicQuery struct {
	Builder[topic, *topicQuery]
}

func topics(ctx context.Context) *topicQuery {
	return NewQuery[topic](ctx, &topicQuery{})
}

func TestResolveRouteBindingFindsTheRowByTheRouteKey(t *testing.T) {
	ctx, conn, _ := schemaCase(t)
	conn.rows = rowsOf([]string{"id", "uuid", "owner_id"}, []any{int64(1), ticketUUID, int64(7)})

	found, err := tickets(ctx).ResolveRouteBinding(ticketUUID)
	if want := (ticket{ID: 1, UUID: ticketUUID, OwnerID: 7}); err != nil || found != want {
		t.Fatalf("ResolveRouteBinding = %+v (%v), want %+v", found, err, want)
	}

	if want := `select * from "tickets" where "tickets"."uuid" = $1 limit 1`; conn.sql != want || !reflect.DeepEqual(conn.args, []any{ticketUUID}) {
		t.Fatalf("sql = %s %v, want %s [%s]", conn.sql, conn.args, want, ticketUUID)
	}
}

// Without a declared route key a model binds by its id, the parameter
// parsed as the column's integer.
func TestResolveRouteBindingDefaultsToTheID(t *testing.T) {
	ctx, conn, _ := schemaCase(t)

	_, _ = accounts(ctx).ResolveRouteBinding("7")

	if want := `select * from "accounts" where "accounts"."id" = $1 limit 1`; conn.sql != want || !reflect.DeepEqual(conn.args, []any{int64(7)}) {
		t.Fatalf("sql = %s %v, want %s [7]", conn.sql, conn.args, want)
	}
}

// Any valid UTF-8 a text key holds is bound as written.
func TestResolveRouteBindingBindsATextKeyAsWritten(t *testing.T) {
	ctx, conn, _ := schemaCase(t)

	_, _ = topics(ctx).ResolveRouteBinding("côte-d'ivoire")

	if want := `select * from "topics" where "topics"."slug" = $1 limit 1`; conn.sql != want || !reflect.DeepEqual(conn.args, []any{"côte-d'ivoire"}) {
		t.Fatalf("sql = %s %v, want %s [côte-d'ivoire]", conn.sql, conn.args, want)
	}
}

// The query it's called on scopes the binding: a row outside the scope is
// as missing as one that doesn't exist.
func TestResolveRouteBindingKeepsTheQuerysScope(t *testing.T) {
	ctx, conn, _ := schemaCase(t)
	conn.rows = rowsOf([]string{"id", "uuid", "owner_id"})

	_, err := tickets(ctx).Where("owner_id", 7).ResolveRouteBinding(ticketUUID)

	var missing *ModelNotFoundError
	if !errors.As(err, &missing) || missing.Error() != "ticket not found." {
		t.Fatalf("err = %v, want the ticket's ModelNotFoundError", err)
	}

	if want := `select * from "tickets" where "owner_id" = $1 and "tickets"."uuid" = $2 limit 1`; conn.sql != want || !reflect.DeepEqual(conn.args, []any{7, ticketUUID}) {
		t.Fatalf("sql = %s %v, want %s", conn.sql, conn.args, want)
	}
}

// A value the route key's column can't hold names no row: the same 404,
// with no query sent for Postgres to reject.
func TestAValueTheRouteKeyCantHoldIsMissingWithoutAQuery(t *testing.T) {
	ctx, conn, _ := schemaCase(t)

	cases := []struct {
		name string
		bind func() error
	}{
		{"text for a uuid", func() error { _, err := tickets(ctx).ResolveRouteBinding("not-a-uuid"); return err }},
		{"nothing for a uuid", func() error { _, err := tickets(ctx).ResolveRouteBinding(""); return err }},
		{"text for an id", func() error { _, err := accounts(ctx).ResolveRouteBinding("not-a-number"); return err }},
		{"a fraction for an id", func() error { _, err := accounts(ctx).ResolveRouteBinding("1.5"); return err }},
		{"an id past int8", func() error { _, err := accounts(ctx).ResolveRouteBinding("9223372036854775808"); return err }},
		{"a NUL for a text key", func() error { _, err := topics(ctx).ResolveRouteBinding("spain\x00"); return err }},
		{"invalid UTF-8 for a text key", func() error { _, err := topics(ctx).ResolveRouteBinding("\xff"); return err }},
	}

	for _, c := range cases {
		conn.sql = ""

		var missing *ModelNotFoundError
		if err := c.bind(); !errors.As(err, &missing) || missing.HTTPStatus() != 404 {
			t.Errorf("%s: err = %v, want a ModelNotFoundError", c.name, err)
		}

		if conn.sql != "" {
			t.Errorf("%s: sent %s, want no query", c.name, conn.sql)
		}
	}
}

func TestResolveRouteBindingNeedsAModel(t *testing.T) {
	defer func() {
		if recover() == nil {
			t.Fatal("ResolveRouteBinding on a table didn't panic")
		}
	}()

	_, _ = Table[ticket](context.Background(), "tickets").ResolveRouteBinding(ticketUUID)
}
