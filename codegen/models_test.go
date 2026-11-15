package codegen

import (
	"strings"
	"testing"
)

func TestNamingFollowsGoConventions(t *testing.T) {
	for in, want := range map[string]string{"airport_id": "AirportID", "image_url": "ImageURL", "unique_id": "UniqueID", "eu_vat_code": "EuVatCode", "iata": "Iata"} {
		if got := pascal(in); got != want {
			t.Errorf("pascal(%q) = %q, want %q", in, got, want)
		}
	}

	for in, want := range map[string]string{"City": "Cities", "Search": "Searches", "Coach": "Coaches", "Airport": "Airports", "Gateway": "Gateways", "FareCategory": "FareCategories", "TerminalAlias": "TerminalAliases"} {
		if got := pluralize(in); got != want {
			t.Errorf("pluralize(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestGeneratesStructQueryAndRelations(t *testing.T) {
	spec := Spec{Package: "models", Models: map[string]ModelSpec{
		"airports": {Name: "Airport"},
		"flights":  {Name: "Flight"},
	}}
	schema := Schema{
		Columns: map[string][]Column{
			"airports": {{Name: "id", UDT: "int8"}, {Name: "name", UDT: "varchar"}},
			"flights": {
				{Name: "id", UDT: "int8"},
				{Name: "origin_airport_id", UDT: "int8", Nullable: true},
				{Name: "destination_airport_id", UDT: "int8"},
				{Name: "cancelled_at", UDT: "timestamptz", Nullable: true},
				{Name: "payload", UDT: "jsonb", Nullable: true},
			},
		},
		ForeignKeys: []ForeignKey{
			{Table: "flights", Column: "origin_airport_id", RefTable: "airports", RefColumn: "id"},
			{Table: "flights", Column: "destination_airport_id", RefTable: "airports", RefColumn: "id"},
		},
	}

	files, err := Generate(spec, schema)
	if err != nil {
		t.Fatal(err)
	}

	flight := squash(string(files["flight_gen.go"]))
	for _, want := range []string{
		"CancelledAt *time.Time `db:\"cancelled_at\"`",
		"Payload json.RawMessage `db:\"payload\"`",
		"func Flights(ctx context.Context) *FlightQuery",
		// Nullable FK: no row, rather than `id = NULL`.
		"if f.OriginAirportID == nil {",
		"return Airports(ctx).WhereKey(f.DestinationAirportID)",
		// Each belongs-to is eager-loaded into Loaded, keyed by the FK: a
		// nullable one as it is, a non-null one by its address.
		"Loaded FlightRelations `db:\"-\" json:\"-\"`",
		"type FlightRelations struct { DestinationAirport *Airport OriginAirport *Airport }",
		`func (q *FlightQuery) WithOriginAirport(scopes ...func(q *AirportQuery)) *FlightQuery {
			return q.Eager(db.BelongsTo(Airports, scopes, func(f *Flight) (*int64, **Airport) { return f.OriginAirportID, &f.Loaded.OriginAirport }))
		}`,
		`func (q *FlightQuery) WithDestinationAirport(scopes ...func(q *AirportQuery)) *FlightQuery {
			return q.Eager(db.BelongsTo(Airports, scopes, func(f *Flight) (*int64, **Airport) { return &f.DestinationAirportID, &f.Loaded.DestinationAirport }))
		}`,
	} {
		if !strings.Contains(flight, squash(want)) {
			t.Errorf("flight_gen.go is missing %q:\n%s", want, flight)
		}
	}

	airport := squash(string(files["airport_gen.go"]))
	for _, want := range []string{
		`func (a Airport) FlightsByOriginAirport(ctx context.Context) *FlightQuery`,
		`return Flights(ctx).Where(FlightTableColumns.DestinationAirportID, a.ID)`,
	} {
		if !strings.Contains(airport, squash(want)) {
			t.Errorf("airport_gen.go is missing %q:\n%s", want, airport)
		}
	}

	// A model that belongs to nothing has nothing to load.
	for _, unwanted := range []string{"Loaded", "Relations", "func (q *AirportQuery) With"} {
		if strings.Contains(airport, unwanted) {
			t.Errorf("airport_gen.go has %q:\n%s", unwanted, airport)
		}
	}
}

// Each model names its columns twice over one type: bare, for a query on the
// table alone and a write's keys, and qualified by the table, for a scope or
// a join.
func TestGeneratesBareAndQualifiedColumnIdentifiers(t *testing.T) {
	files, err := Generate(
		Spec{Package: "models", Models: map[string]ModelSpec{
			"airports": {Name: "Airport"},
			"flights":  {Name: "Flight"},
		}},
		Schema{Columns: map[string][]Column{
			"airports": {{Name: "id", UDT: "int8"}, {Name: "name", UDT: "varchar"}},
			"flights": {
				{Name: "id", UDT: "int8"},
				{Name: "destination_airport_id", UDT: "int8"},
				{Name: "cancelled_at", UDT: "timestamptz", Nullable: true},
			},
		}},
	)
	if err != nil {
		t.Fatal(err)
	}

	flight := squash(string(files["flight_gen.go"]))
	for _, want := range []string{
		`// flightColumns names each column of ` + "`flights`" + `.
		type flightColumns struct { ID string DestinationAirportID string CancelledAt string }`,
		`var FlightColumns = flightColumns{ ID: "id", DestinationAirportID: "destination_airport_id", CancelledAt: "cancelled_at", }`,
		`var FlightTableColumns = flightColumns{ ID: "flights.id", DestinationAirportID: "flights.destination_airport_id", CancelledAt: "flights.cancelled_at", }`,
	} {
		if !strings.Contains(flight, squash(want)) {
			t.Errorf("flight_gen.go is missing %q:\n%s", want, flight)
		}
	}

	airport := squash(string(files["airport_gen.go"]))
	for _, want := range []string{
		`type airportColumns struct { ID string Name string }`,
		`var AirportColumns = airportColumns{ ID: "id", Name: "name", }`,
		`var AirportTableColumns = airportColumns{ ID: "airports.id", Name: "airports.name", }`,
	} {
		if !strings.Contains(airport, squash(want)) {
			t.Errorf("airport_gen.go is missing %q:\n%s", want, airport)
		}
	}
}

// A model keyed by a non-null id updates, refreshes and deletes its own row;
// Update and Refresh read the row back into the struct, keeping what its
// query loaded.
func TestAKeyedModelWritesItsOwnRow(t *testing.T) {
	spec := Spec{Package: "models", Models: map[string]ModelSpec{
		"airports":          {Name: "Airport"},
		"airport_terminals": {Name: "AirportTerminal"},
		"terminal_aliases":  {Name: "TerminalAlias"},
		"draft_snapshots":   {Name: "DraftSnapshot"},
	}}
	schema := Schema{
		Columns: map[string][]Column{
			"airports":          {{Name: "id", UDT: "int8"}, {Name: "name", UDT: "varchar"}},
			"airport_terminals": {{Name: "id", UDT: "uuid"}, {Name: "airport_id", UDT: "int8"}},
			"terminal_aliases":  {{Name: "unique_id", UDT: "uuid"}},
			"draft_snapshots":   {{Name: "id", UDT: "int8", Nullable: true}},
		},
		ForeignKeys: []ForeignKey{{Table: "airport_terminals", Column: "airport_id", RefTable: "airports", RefColumn: "id"}},
	}

	files, err := Generate(spec, schema)
	if err != nil {
		t.Fatal(err)
	}

	city := squash(string(files["airport_terminal_gen.go"]))
	for _, want := range []string{
		`"fmt"`,
		`// Update writes values to the row, unless none of them differs from what is
		// stored, and reads it back as stored, keeping the relations in Loaded. An
		// encrypted value always differs: each seal is new.
		func (a *AirportTerminal) Update(ctx context.Context, values db.Values) error {
			updated, err := AirportTerminals(ctx).WhereKey(a.ID).UpdateChanged(values)
			if err != nil {
				return fmt.Errorf("updating airport terminal %v: %w", a.ID, err)
			}

			if len(updated) == 1 {
				updated[0].Loaded = a.Loaded
				*a = updated[0]
			}

			return nil
		}`,
		`// Refresh reads the row again. The relations in Loaded stay as loaded: load
		// them again if their keys changed.
		func (a *AirportTerminal) Refresh(ctx context.Context) error {
			fresh, err := AirportTerminals(ctx).FindOrFail(a.ID)
			if err != nil {
				return fmt.Errorf("refreshing airport terminal %v: %w", a.ID, err)
			}

			fresh.Loaded = a.Loaded
			*a = fresh

			return nil
		}`,
		`func (a *AirportTerminal) Delete(ctx context.Context) error {
			if _, err := AirportTerminals(ctx).WhereKey(a.ID).Delete(); err != nil {
				return fmt.Errorf("deleting airport terminal %v: %w", a.ID, err)
			}

			return nil
		}`,
	} {
		if !strings.Contains(city, squash(want)) {
			t.Errorf("airport_terminal_gen.go is missing %q:\n%s", want, city)
		}
	}

	// A model that loads nothing has no relations to keep.
	airport := squash(string(files["airport_gen.go"]))
	for _, want := range []string{
		`// stored, and reads it back as stored. An encrypted value always differs:
		// each seal is new.
		func (a *Airport) Update(`,
		`if len(updated) == 1 { *a = updated[0] }`,
		`// Refresh reads the row again.
		func (a *Airport) Refresh(`,
		`} *a = fresh`,
	} {
		if !strings.Contains(airport, squash(want)) {
			t.Errorf("airport_gen.go must read its row back without Loaded, missing %q:\n%s", want, airport)
		}
	}

	// A row without a non-null id has no key to write it by.
	for _, file := range []string{"terminal_alias_gen.go", "draft_snapshot_gen.go"} {
		for _, unwanted := range []string{`"fmt"`, ") Update(", ") Refresh(", ") Delete("} {
			if strings.Contains(string(files[file]), unwanted) {
				t.Errorf("%s has %q:\n%s", file, unwanted, files[file])
			}
		}
	}
}

func TestAColumnNamedLikeAGeneratedMethodFailsGeneration(t *testing.T) {
	for _, column := range []string{"loaded", "update", "refresh", "delete"} {
		_, err := Generate(
			Spec{Package: "models", Models: map[string]ModelSpec{"things": {Name: "Thing"}}},
			Schema{Columns: map[string][]Column{"things": {{Name: "id", UDT: "int8"}, {Name: column, UDT: "bool"}}}},
		)
		if err == nil || !strings.Contains(err.Error(), "column "+column+" collides") {
			t.Errorf("err = %v, want the %s column refused", err, column)
		}
	}
}

func TestDeclaredCastsShapeTheFieldsAndTheSchema(t *testing.T) {
	spec := Spec{Package: "models", JSONEncoder: "example.com/dialect.Marshal", Models: map[string]ModelSpec{
		"profiles": {Name: "Profile", Casts: map[string]string{
			"loyalty_number": "encrypted", "birth_date": "encrypted", "emergency_contacts": "encrypted:json", "settings": "json",
		}},
	}}
	schema := Schema{Columns: map[string][]Column{"profiles": {
		{Name: "id", UDT: "int8"},
		{Name: "birth_date", UDT: "text"},
		{Name: "loyalty_number", UDT: "text", Nullable: true},
		{Name: "emergency_contacts", UDT: "text", Nullable: true},
		{Name: "settings", UDT: "json", Nullable: true},
	}}}

	files, err := Generate(spec, schema)
	if err != nil {
		t.Fatal(err)
	}

	profile := squash(string(files["profile_gen.go"]))
	for _, want := range []string{
		"BirthDate string `db:\"birth_date\"`",
		"LoyaltyNumber *string `db:\"loyalty_number\"`",
		"EmergencyContacts json.RawMessage `db:\"emergency_contacts\"`",
		`"example.com/dialect"`,
		`func (Profile) Schema() *db.Schema { return &profileSchema }`,
		`"id": {Type: "int8"},`,
		`"birth_date": {Type: "text", Cast: db.Encrypted},`,
		`"emergency_contacts": {Type: "text", Cast: db.EncryptedJSON(dialect.Marshal)},`,
		`"settings": {Type: "json", Cast: db.JSON(dialect.Marshal)},`,
	} {
		if !strings.Contains(profile, squash(want)) {
			t.Errorf("profile_gen.go is missing %q:\n%s", want, profile)
		}
	}
}

// A `json:<Type>` cast types the field as the named models-package type,
// never a pointer, and keeps the json cast's encoder for writes.
func TestATypedJSONCastTypesTheField(t *testing.T) {
	files, err := Generate(
		Spec{Package: "models", JSONEncoder: "example.com/dialect.Marshal", Models: map[string]ModelSpec{
			"flights": {Name: "Flight", Casts: map[string]string{"seat_map": "json:SeatLayout"}},
		}},
		Schema{Columns: map[string][]Column{"flights": {
			{Name: "id", UDT: "int8"},
			{Name: "seat_map", UDT: "json", Nullable: true},
		}}},
	)
	if err != nil {
		t.Fatal(err)
	}

	flight := squash(string(files["flight_gen.go"]))
	for _, want := range []string{
		"SeatMap SeatLayout `db:\"seat_map\"`",
		`"seat_map": {Type: "json", Cast: db.JSON(dialect.Marshal)},`,
		`"example.com/dialect"`,
	} {
		if !strings.Contains(flight, squash(want)) {
			t.Errorf("flight_gen.go is missing %q:\n%s", want, flight)
		}
	}

	if strings.Contains(flight, `"encoding/json"`) {
		t.Errorf("flight_gen.go imports encoding/json for a typed field:\n%s", flight)
	}
}

func TestTimestampsAreTheTablesUnlessTheSpecTurnsThemOff(t *testing.T) {
	columns := []Column{{Name: "id", UDT: "int8"}, {Name: "created_at", UDT: "timestamp", Nullable: true}, {Name: "updated_at", UDT: "timestamp", Nullable: true}}
	off := false

	files, err := Generate(
		Spec{Package: "models", Models: map[string]ModelSpec{
			"posts":    {Name: "Post"},
			"aliases":  {Name: "Alias"},
			"messages": {Name: "Message", Timestamps: &off},
		}},
		Schema{Columns: map[string][]Column{
			"posts":    columns,
			"aliases":  columns[:2],
			"messages": columns,
		}},
	)
	if err != nil {
		t.Fatal(err)
	}

	for file, want := range map[string]struct{ created, updated bool }{
		"post_gen.go":    {true, true},
		"alias_gen.go":   {true, false},
		"message_gen.go": {false, false},
	} {
		// Probed on the schema block: the column identifiers spell the same keys.
		source := squash(string(files[file]))
		if got := strings.Contains(source, `db.Schema{ CreatedAt: "created_at",`); got != want.created {
			t.Errorf("%s stamps created_at = %v, want %v", file, got, want.created)
		}
		if got := strings.Contains(source, `UpdatedAt: "updated_at", Columns:`); got != want.updated {
			t.Errorf("%s stamps updated_at = %v, want %v", file, got, want.updated)
		}
	}
}

// A declared route key lands in the schema ResolveRouteBinding reads; an
// undeclared one leaves it to default to id.
func TestADeclaredRouteKeyIsTheSchemas(t *testing.T) {
	files, err := Generate(
		Spec{Package: "models", Models: map[string]ModelSpec{
			"fare_rules": {Name: "FareRule", RouteKey: "uuid"},
			"profiles":   {Name: "Profile"},
		}},
		Schema{Columns: map[string][]Column{
			"fare_rules": {{Name: "id", UDT: "int8"}, {Name: "uuid", UDT: "uuid"}},
			"profiles":   {{Name: "id", UDT: "int8"}},
		}},
	)
	if err != nil {
		t.Fatal(err)
	}

	if flight := squash(string(files["fare_rule_gen.go"])); !strings.Contains(flight, `var fareRuleSchema = db.Schema{ RouteKey: "uuid", Columns:`) {
		t.Errorf("fare_rule_gen.go doesn't declare its route key:\n%s", flight)
	}

	if profile := string(files["profile_gen.go"]); strings.Contains(profile, "RouteKey") {
		t.Errorf("profile_gen.go declares a route key it wasn't given:\n%s", profile)
	}
}

func TestABadRouteKeyFailsGeneration(t *testing.T) {
	for name, tc := range map[string]struct{ routeKey, want string }{
		"a missing column":   {routeKey: "slug", want: "route key slug is not a column"},
		"a timestamp column": {routeKey: "created_at", want: `route key created_at is Postgres "timestamp"`},
		"a json column":      {routeKey: "settings", want: `route key settings is Postgres "jsonb"`},
	} {
		_, err := Generate(
			Spec{Package: "models", Models: map[string]ModelSpec{"things": {Name: "Thing", RouteKey: tc.routeKey}}},
			Schema{Columns: map[string][]Column{"things": {{Name: "id", UDT: "int8"}, {Name: "created_at", UDT: "timestamp"}, {Name: "settings", UDT: "jsonb"}}}},
		)
		if err == nil || !strings.Contains(err.Error(), tc.want) {
			t.Errorf("%s: err = %v, want it to mention %q", name, err, tc.want)
		}
	}
}

func TestABadCastFailsGeneration(t *testing.T) {
	for name, tc := range map[string]struct {
		casts   map[string]string
		encoder string
		want    string
	}{
		"an unknown cast":                {casts: map[string]string{"notes": "hashed"}, want: `no cast "hashed"`},
		"a cast on a missing column":     {casts: map[string]string{"nickname": "encrypted"}, want: "nickname"},
		"encrypted on a non-text column": {casts: map[string]string{"settings": "encrypted"}, want: "needs a text column"},
		"json on a text column":          {casts: map[string]string{"notes": "json"}, want: "needs a json column"},
		"an encoder without a func":      {casts: map[string]string{"settings": "json"}, encoder: "example.com/json", want: "not an import/path.Func"},
		"a typed json cast with no type": {casts: map[string]string{"settings": "json:"}, want: `exported type of the models package, as json:ChildAges, not ""`},
		"an unexported json type":        {casts: map[string]string{"settings": "json:childAges"}, want: `not "childAges"`},
		"a json type from a package":     {casts: map[string]string{"settings": "json:data.ChildAges"}, want: `not "data.ChildAges"`},
		"a typed json on a text column":  {casts: map[string]string{"notes": "json:ChildAges"}, want: "needs a json column"},
		"a typed encrypted json":         {casts: map[string]string{"notes": "encrypted:json:ChildAges"}, want: `no cast "encrypted:json:ChildAges"`},
	} {
		_, err := Generate(
			Spec{Package: "models", JSONEncoder: tc.encoder, Models: map[string]ModelSpec{"things": {Name: "Thing", Casts: tc.casts}}},
			Schema{Columns: map[string][]Column{"things": {{Name: "id", UDT: "int8"}, {Name: "notes", UDT: "text"}, {Name: "settings", UDT: "jsonb"}}}},
		)
		if err == nil || !strings.Contains(err.Error(), tc.want) {
			t.Errorf("%s: err = %v, want it to mention %q", name, err, tc.want)
		}
	}
}

func TestUnknownPostgresTypesFailGeneration(t *testing.T) {
	_, err := Generate(
		Spec{Package: "models", Models: map[string]ModelSpec{"things": {Name: "Thing"}}},
		Schema{Columns: map[string][]Column{"things": {{Name: "id", UDT: "int8"}, {Name: "shape", UDT: "geometry"}}}},
	)
	if err == nil || !strings.Contains(err.Error(), "geometry") {
		t.Fatalf("err = %v", err)
	}
}

// squash collapses whitespace runs, so assertions survive gofmt alignment.
func squash(s string) string {
	return strings.Join(strings.Fields(s), " ")
}
