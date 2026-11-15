# Validation

- [Introduction](#introduction)
- [Validation Quickstart](#validation-quickstart)
    - [Defining the Routes](#defining-the-routes)
    - [Creating the Controller](#creating-the-controller)
    - [Writing the Validation Logic](#writing-the-validation-logic)
    - [Validation Error Response Format](#validation-error-response-format)
    - [A Note on Optional Fields](#a-note-on-optional-fields)
- [Form Request Validation](#form-request-validation)
    - [Creating Form Requests](#creating-form-requests)
    - [Customizing the Error Messages](#customizing-the-error-messages)
    - [Filling the Request](#filling-the-request)
- [Manually Creating Validators](#manually-creating-validators)
    - [Validating Input Without a Form Request](#validating-input-without-a-form-request)
    - [The Validator Instance](#the-validator-instance)
    - [Reading the Input](#reading-the-input)
    - [Ordering the Errors](#ordering-the-errors)
    - [Customizing the Error Messages](#customizing-the-error-messages-1)
    - [Performing Additional Validation](#performing-additional-validation-1)
    - [Failing Validation Yourself](#failing-validation-yourself)
- [Working With Validated Input](#working-with-validated-input)
- [Working With Error Messages](#working-with-error-messages)
- [Available Validation Rules](#available-validation-rules)
- [Conditionally Adding Rules](#conditionally-adding-rules)
- [Validating Arrays](#validating-arrays)
    - [Validating Nested Array Input](#validating-nested-array-input)
    - [Error Messages for Array Elements](#error-messages-for-array-elements)
- [Validating Passwords](#validating-passwords)
- [Custom Validation Rules](#custom-validation-rules)
    - [Using Closures](#using-closures)
    - [Implicit Rules](#implicit-rules)
    - [The Rule and Field Types](#the-rule-and-field-types)

## Introduction

reef gives you a few ways to validate your application's incoming data. Most of the time you will describe a request as a **form request**: a struct whose `json` tags name the input fields, plus a `Rules` method. `web.Validate` checks the request's input against those rules and hands the struct back to you, filled and typed. When there is no request to describe, such as a console command checking a file, create a `validation.Validator` yourself.

reef includes a focused set of validation rules, among them database rules that check whether a value exists in, or is unique to, a table. Rules are Go values rather than strings, so the compiler checks their names and arguments: where Laravel writes `'required|string|max:255'`, you write `{rule.Required, rule.String, rule.Max(255)}`. The default messages read like Laravel's English ones ("The email field is required."), and a failure answers with the same 422 JSON body a Laravel API sends.

Validation lives in three packages:

- `github.com/atobeach/reef/validation/rule` holds the rules and `rule.Set`, the map from attribute to rules.
- `github.com/atobeach/reef/validation` runs a `rule.Set` over input: the `Validator` (and its `Decode`), the `Input` it reads, the `Errors` message bag, `Decode`, `FieldOrder` and `CheckFields`.
- `github.com/atobeach/reef/web` connects validation to requests: the `FormRequest` interface, `Validate`, `CheckFormRequest`, `ValidatedInput`, `ValidationError` and `ValidationFailed`.

## Validation Quickstart

To learn about reef's validation features, let's look at a complete example of validating a request and returning the error messages to the frontend: a form that searches for flights.

### Defining the Routes

First, let's assume we have the following route defined in `routes/search.go`:

```go
r.Post("/flights/search", c.FlightSearch.Store).Name("flights.search.store")
```

The `POST` route starts a flight search from the form's input. To learn more about defining routes, see the [routing documentation](routing.md).

### Creating the Controller

Next, let's take a look at a simplified version of the controller that handles the route. A real controller would do a little more (remember the search, render a failed one); those steps are left out here:

```go
package controllers

import (
	"example.com/flights/app/actions"
	"example.com/flights/app/data/flightsearch"
	"example.com/flights/app/http/requests"
	"github.com/atobeach/reef/web"
)

// FlightSearchController starts flight searches.
type FlightSearchController struct {
	Run *actions.RunFlightSearch
	// ...
}

// Store starts a flight search.
func (ctl *FlightSearchController) Store(c *web.Context) (flightsearch.FlightSearchOutputData, error) {
	request, err := web.Validate[requests.FlightSearchRequest](c)
	if err != nil {
		return flightsearch.FlightSearchOutputData{}, err
	}

	// The input is valid: request.Origin, request.Adults and
	// request.ChildAges hold the typed values. searchInput resolves the
	// airports and dates into the search to run.
	searchInput, err := ctl.searchInput(c, request)
	if err != nil {
		return flightsearch.FlightSearchOutputData{}, err
	}

	result, err := ctl.Run.Handle(c, searchInput)
	if err != nil {
		return flightsearch.FlightSearchOutputData{}, err
	}

	return flightsearch.OutputData(result), nil
}
```

### Writing the Validation Logic

`web.Validate` reads the request's input (`c.All()`: the query string merged with a JSON or form body), checks it against the form request's rules, and, if it passes, decodes it into a `requests.FlightSearchRequest`. Here is that form request, from `app/http/requests` (its `After` hook is shown [below](#performing-additional-validation)):

```go
package requests

import (
	"time"

	"github.com/atobeach/reef/validation/rule"
)

// FlightSearchRequest validates a flight search.
type FlightSearchRequest struct {
	Origin        string  `json:"origin"`
	Destination   string  `json:"destination"`
	DepartureDate string  `json:"departure_date"`
	ReturnDate    *string `json:"return_date"`
	Adults        int     `json:"adults"`
	ChildAges     []int   `json:"child_ages"`
	Currency      *string `json:"currency"`
	Timezone      *string `json:"timezone"`
}

// Rules are the request's rules.
func (FlightSearchRequest) Rules() rule.Set {
	return rule.Set{
		"origin":         {rule.Required, rule.String, rule.Size(3)},
		"destination":    {rule.Required, rule.String, rule.Size(3)},
		"departure_date": {rule.Required, rule.DateFormat(time.DateOnly)},
		"return_date":    {rule.Nullable, rule.DateFormat(time.DateOnly), rule.AfterOrEqual("departure_date")},
		"adults":         {rule.Required, rule.Integer, rule.Min(1), rule.Max(9)},
		"child_ages":     {rule.Nullable, rule.Array, rule.Max(9)},
		"child_ages.*":   {rule.Integer, rule.Min(0), rule.Max(17)},
		"currency":       {rule.Sometimes, rule.String, rule.Size(3)},
		"timezone":       {rule.Sometimes, rule.String, rule.Max(64), rule.Timezone},
	}
}
```

Each key of a `rule.Set` names an attribute, and each value lists that attribute's rules, which run in the order you list them. All of the available rules are [documented below](#available-validation-rules).

If validation fails, `Validate` returns a `*web.ValidationError`. Return it from your controller, as above, and the kernel answers with a `422 Unprocessable Entity` carrying every message. If validation passes, your controller carries on with a filled, typed request.

> [!NOTE]
> A rule that can't run is not a validation failure. When a database rule's query fails (the database is down), `Validate` returns that error instead, and the request gets a `500`: answering with a 422 would blame the user for an outage.

#### Stopping on First Validation Failure

Sometimes you may wish to stop running an attribute's rules after its first failure. To do so, start its rules with `rule.Bail`:

```go
"currency": {rule.Bail, rule.String, rule.Size(3), rule.In("EUR", "GBP", "USD")},
```

In this example, if `currency` isn't a string, the `size` and `in` rules won't run, so the response carries one message for it rather than three. A failed presence rule (`required`, `required_with`, or any other [implicit rule](#implicit-rules)) always stops its attribute, bail or not: the other rules would only restate that the value is missing.

`Bail` works per attribute. There is no switch that stops the whole validator at the first failure; every attribute is always checked.

#### A Note on Nested Attributes

If the incoming request contains nested input, you may refer to nested fields with "dot" syntax:

```go
"departure_times":          {rule.Sometimes, rule.ArrayKeys("outbound", "return")},
"departure_times.outbound": {rule.Nullable, rule.String, rule.In("morning", "afternoon", "evening")},
```

A numeric segment indexes into a list (`passengers.0.first_name`), and `*` matches every element; see [Validating Arrays](#validating-arrays). A dot always means nesting: there is no way to escape a dot that is part of an input key.

### Validation Error Response Format

When validation fails, the response is a `422` with a JSON body like this one, for the booking request used throughout this page:

```json
{
    "message": "The flight id field is required. (and 6 more errors)",
    "errors": {
        "flight_id": ["The flight id field is required."],
        "contact_email": ["Please enter a valid email address."],
        "phone_number": ["Phone number must contain only digits."],
        "passengers.0.passenger_type": ["The selected passengers.0.passenger_type is invalid."],
        "passengers.0.first_name": ["First name is required."],
        "passengers.0.last_name": ["Last name is required."],
        "passengers.0.birth_date": ["Birth date must be in DD/MM/YYYY format."]
    }
}
```

- `errors` is keyed by the attribute as validated. Nested and array attributes use dot notation (`passengers.0.first_name`), one key per element.
- An attribute's messages are in the order its rules ran. A message already recorded for the attribute is not repeated.
- Attributes are listed in the order the request struct declares its fields, every plain attribute before every array-element one. See [Ordering the Errors](#ordering-the-errors).
- `message` is the first error, followed by "(and 1 more error)" or "(and N more errors)", counting messages rather than attributes.

reef always answers a validation failure with JSON. There is no redirect back to a form with flashed errors and old input: the frontend owns its form state and renders the messages it receives.

To assert on these responses in a feature test, use `AssertJSONValidationErrors` and its siblings, described in [HTTP Tests](http-tests.md#testing-validation). Here `tests.Setup` and `tc.ActingAs` are helpers from your application's own [test case](testing.md#a-full-test-case):

```go
func TestMalformedAirportCodesAndAMissingLabelAreRejected(t *testing.T) {
	tc := tests.Setup(t)
	user := factories.User(tc).Create()
	browser := tc.ActingAs(user)

	browser.PutJSON("/user/favourite-airport", map[string]any{"code": "HEATHROW", "label": "London Heathrow"}).AssertJSONValidationErrors("code")
	browser.PutJSON("/user/favourite-airport", map[string]any{"code": "LHR"}).AssertJSONValidationError("label", "The label field is required when code is present.")
}
```

### A Note on Optional Fields

reef's input layer trims every string (except `password`, `password_confirmation` and `current_password`) and turns blank strings into `nil` (see [Input Trimming and Normalization](requests.md#input-trimming-and-normalization)). Because of this, you will often need to mark your "optional" fields as `rule.Nullable` if you do not want the validator to consider `null` invalid:

```go
"middle_name": {rule.Nullable, rule.String, rule.Max(255)},
```

Here is how each kind of missing value meets an attribute's rules. Implicit rules are `required`, `required_with` and any custom rule marked [implicit](#implicit-rules):

| The value is | Implicit rules | Other rules |
|---|---|---|
| absent from the input | run, unless the attribute has `rule.Sometimes` | skipped |
| `null` | run | skipped if the attribute has `rule.Nullable`, otherwise run |
| a blank string | run | skipped |

So `{rule.String, rule.Max(255)}` already lets the field be left out, but rejects an explicit `null` with "The middle name field must be a string."; adding `rule.Nullable` accepts the `null` too.

## Form Request Validation

### Creating Form Requests

A form request is a struct type that describes one request's input and its rules. reef has no generator for them: a typical application keeps one per file in `app/http/requests`, each a struct whose `json` tags name the input fields and a `Rules` method returning its `rule.Set`. Any struct with that method satisfies `web.FormRequest`:

```go
type FormRequest interface {
	Rules() rule.Set
}
```

A form request may also declare any of these optional methods, which `Validate` finds and uses:

```go
Messages() map[string]string                         // custom messages (value receiver)
Attributes() map[string]string                       // custom attribute names (value receiver)
After(c *web.Context, errs *validation.Errors) error // checks on the decoded request
```

To validate, call `web.Validate` with the form request's type, as the quickstart's controller does. On success you get the filled struct back:

```go
request, err := web.Validate[requests.StoreBookingRequest](c)
if err != nil {
	return nil, err
}
```

The error is a `*web.ValidationError` when the input is invalid (a 422), the input layer's own error when the request couldn't be read (see [How Input Is Parsed](requests.md#how-input-is-parsed)), a rule's or hook's error when one couldn't run (a 500), or the reason the form request itself is malformed (a 500, see below).

> [!WARNING]
> `Validate` calls `Rules`, `Messages` and `Attributes` on a zero value of the request, so declare them with value receivers, and don't make the rules depend on the struct's fields: they are empty when the rules are built. `Validate` refuses a `Messages` or `Attributes` method on a pointer receiver, and an `After` whose signature isn't the one above, since either would otherwise never run: the request answers a `500` until it is fixed. `After` may use a value or a pointer receiver. The type argument must be the struct type itself: `web.Validate[*requests.StoreBookingRequest]` compiles, but fails at runtime with an error the kernel renders as a `500`.

> [!NOTE]
> Form requests have no `authorize` or `prepareForValidation` methods. Check authorization in route [middleware](middleware.md) or in the controller before validating. To adjust the input first, [merge](requests.md#merging-additional-input) the new values into it with `c.Merge` before calling `Validate`, as a registration controller might lowercase the email; the input layer already trims and nulls blank strings. A value you derive from the validated input belongs in a method on the request struct, such as a `StoreBookingRequest.Booking()` that hands `actions.BookFlight` its typed input.

#### Rules and Fields Agree

A form request's struct is its validated input, so its rules and its fields must describe the same keys. `Validate` checks this once per type, before reading any input, and answers a `500` (reported, like any server error) while the two disagree:

- **Every rule names a field.** A rule key must be one of the struct's paths, `json` names joined with dots and `*` for a list element (`passengers.*.first_name`). A key inside a field kept as sent, an `any`, a map or a type that decodes itself, names that field: if a `PaymentWebhook.Payload` field is a `json.RawMessage`, a `webhook.payload.id` key is fine.
- **Every field has a rule.** A field needs a rule key of its own, even one holding only `rule.Sometimes` or `rule.Nullable`. A struct field is exempt, since its own fields carry the rules, and so is a list element (`child_ages.*` may have rules, but needs none). A list field is not: its `Array` and `Max` rules are what bound it.
- **Every field can be filled.** A field no input can fill is refused: a channel, a function, a complex number, a fixed-size array, an interface with methods, or a map whose keys aren't strings.

`web.CheckFormRequest[T]()` runs the same checks and returns what it found. A single test in `app/http/requests` can check every form request with it, so a renamed `json` tag without its rule key fails before any request does:

```go
func TestEveryFormRequestsRulesAndFieldsAgree(t *testing.T) {
	for name, check := range map[string]func() error{
		"StoreBookingRequest": web.CheckFormRequest[requests.StoreBookingRequest],
		// ...one entry per form request
	} {
		if err := check(); err != nil {
			t.Errorf("%s:\n%v", name, err)
		}
	}
}
```

It reports every problem it finds, joined into one error: the field problems sorted (`validation: rule "seat_preference" names no field of requests.StoreBookingRequest`, `validation: field "remember" of requests.RegisterRequest has no rule`), then any misdeclared optional method (`web: requests.StoreBookingRequest's Messages must be Messages() map[string]string, on a value receiver`). Because `Validate` itself checks every optional method's receiver and signature, a form request carries no `var _ = ...` assertion for them.

The field half of these checks is `validation.CheckFields(t, rules)`, which takes the struct's `reflect.Type` and its `rule.Set` and returns `nil` when they agree. You may call it for a struct you fill with a [validator of your own](#the-validator-instance).

#### Performing Additional Validation

Sometimes you need to check something after the rules have run, such as comparing a date against the traveller's own "today". Give the form request an `After` method. Its receiver is the decoded request, so it reads typed fields; it is handed the request and the message bag:

```go
// After rejects a departure date that has passed in the traveller's
// timezone.
func (r FlightSearchRequest) After(c *web.Context, errs *validation.Errors) error {
	// The hook runs even when the rules failed: a malformed date can't be
	// compared, and it already has its message.
	if errs.HasAny("departure_date", "timezone") {
		return nil
	}

	location, err := time.LoadLocation(support.Deref(r.Timezone))
	if err != nil {
		location = time.UTC
	}

	today := clock.Now(c).In(location).Format(time.DateOnly)
	if r.DepartureDate < today {
		errs.Add("departure_date", "The departure date has already passed.")
	}

	return nil
}
```

A few things to keep in mind when writing a hook:

- It runs once the rules have, **even when they failed**, so check `errs.HasAny(...)` before relying on a field.
- The struct is filled before the rules run. A value its field couldn't hold left the field's zero value (a `nil` slice or pointer, `""`, `0`), so a field whose attribute has an error holds whatever fit, not necessarily what was sent.
- Add a message for invalid input with `errs.Add`. Messages a hook adds are listed after the rules' messages.
- Return an error only when the check couldn't run (a lookup failed). It stops validation and the request gets a `500`.
- `c` is for the request's facts: a hook may read the [clock](helpers.md#dates-and-time) and query models through it, and read what route middleware bound to it.

A booking request might need both: its hook counts the decoded passengers, loads the flight's fare that a route middleware resolved for the request, and compares the submitted party with the one the fare was priced for:

```go
// After checks the party's adult/child split matches the priced one.
func (r StoreBookingRequest) After(c *web.Context, errs *validation.Errors) error {
	// Absent, null or non-list passengers decode as nil and already failed
	// their own rules; an empty list still meets the party check.
	if r.PassengerInputs == nil {
		return nil
	}

	// A failed lookup fails the request: skipping the check could book the
	// wrong party.
	fare, err := appmiddleware.RequestFare(c)
	if err != nil || fare == nil {
		return err
	}

	adults, children := 0, 0
	for _, passenger := range r.PassengerInputs {
		switch passenger.PassengerType {
		case "Adult":
			adults++
		case "Child", "Infant":
			children++
		}
	}

	if adults != fare.Adults || children != fare.Children {
		errs.Add("passengers", fmt.Sprintf(
			"Expected %d adult(s) and %d child(ren) but received %d and %d.",
			fare.Adults, fare.Children, adults, children,
		))
	}

	return nil
}
```

### Customizing the Error Messages

You may customize the messages a form request uses by giving it a `Messages` method. It returns a map of `attribute.rule` keys to messages:

```go
// Messages are the request's custom messages.
func (StoreBookingRequest) Messages() map[string]string {
	return map[string]string{
		"contact_email.required":              "Email is required.",
		"contact_email.email":                 "Please enter a valid email address.",
		"phone_number.regex":                  "Phone number must contain only digits.",
		"passengers.*.first_name.required":    "First name is required.",
		"passengers.*.birth_date.date_format": "Birth date must be in DD/MM/YYYY format.",
	}
}
```

The rule part of a key is the rule's name, given with each rule in the [list below](#available-validation-rules) (`required`, `date_format`, `exists`, or the name you gave a [custom rule](#using-closures)). A key may use `*` wildcards, which match any run of characters, dots included. An exact key wins over a wildcard one, and wildcard keys are tried in sorted order, so overlapping patterns resolve the same way every time.

A message may use the `:attribute` placeholder, along with `:Attribute` and `:ATTRIBUTE` for the capitalized and upper-case forms. These are the only placeholders: a default message already contains its limits ("must be at least 2 characters"), but a custom one has no `:min` or `:max`, so write the number into the text.

#### Customizing the Validation Attributes

The `:attribute` placeholder is replaced with the attribute's display name. By default that is the attribute with its underscores turned into spaces and its camelCase split (`departure_date` reads "departure date", `passenger.firstName` reads "passenger.first name"), while an attribute expanded from a wildcard is shown as it is (`passengers.0.first_name`). To name an attribute yourself, give the form request an `Attributes` method. The `StoreBookingRequest` above writes whole messages instead, but if it named its attributes, the method would look like this:

```go
// Attributes are the request's custom attribute names.
func (StoreBookingRequest) Attributes() map[string]string {
	return map[string]string{
		"contact_email":          "email address",
		"passengers.*.last_name": "last name",
	}
}
```

Keys are matched like message keys: exactly first, then as wildcards. A custom name also applies wherever another rule names the attribute, as in `after` and `required_with` messages.

### Filling the Request

`Validate` decodes the input into the form request by `json` tag first, before the rules run, with the validator's `Decode`. Decoding is more forgiving than `encoding/json`, since query-string and form input arrives as text, and some clients send an object for a list:

- Query-string scalars fill typed fields: `"3"` fills an `int`. A `bool` field reads its value as [`Input.Boolean`](#reading-the-input) does: `true`, `1`, `"1"`, `"true"`, `"on"` and `"yes"` (any case) fill it as `true`, and anything else as `false`.
- `null` sets the field's zero value (a `nil` pointer), and a key that is absent leaves the field untouched.
- Lists fill slices, and so does an object keyed `"0"` to `"n-1"`. Objects fill structs and maps, and so does an empty list, since some clients send `[]` for `{}`.
- An `any` field receives the value as the input holds it (a JSON number arrives as a `json.Number`).
- A type that implements `json.Unmarshaler` (`json.RawMessage`, `clock.Time`) is decoded by `encoding/json`.
- Strings are taken as the input layer left them, so a password keeps its surrounding whitespace.
- An untagged embedded struct's fields count as the outer struct's own, as with `encoding/json`. Unexported fields and fields tagged `json:"-"` are skipped; an exported field without a tag uses its Go name.

Decoding fills what fits. A value a field can't hold leaves the field's zero value, and decoding carries on with the next field or element, so one bad element leaves a zero element in an otherwise decoded list. The value is a mismatch, named by the rule the field's type implies:

| The field is | The value it can't hold | Reported as | Message |
|---|---|---|---|
| a slice | not a list, nor an object keyed `"0"` to `"n-1"` | `list` | The :attribute field must be a list. |
| a struct or a map | not an object, nor `[]` | `array` | The :attribute field must be an array. |
| a string | an object or a list (other scalars become text) | `string` | The :attribute field must be a string. |
| an integer | text that isn't a number (or a negative one, for an unsigned field) | `integer` | The :attribute field must be an integer. |
| a float | text that isn't a number | `numeric` | The :attribute field must be a number. |
| a `json.Unmarshaler` | anything its `UnmarshalJSON` refuses | `format` | The :attribute field format is invalid. |

A `bool` or `any` field never mismatches. A mismatch is reported on its concrete path, `airlines` or `passengers.1`, through the same custom messages (`"airlines.list"`) and attribute names a rule's message takes; a path through a list element or map entry is shown as it is, like an attribute expanded from a wildcard.

Mismatches are reported only when the rules and the hooks found nothing: their errors already explain the input, and a value the type can't hold usually breaks a rule too. So `{"airlines": {"x": "BA"}}`, which passes `rule.Array` (any object does), answers:

```json
{
    "message": "The airlines field must be a list.",
    "errors": {
        "airlines": ["The airlines field must be a list."]
    }
}
```

A pointer field tells "sent" from "null or absent", but it can't tell an explicit `null` from a missing key. When that difference matters, embed [`web.ValidatedInput`](#working-with-validated-input).

You may also call `validation.Decode(input, &dst)` yourself. It fills what fits and returns one error naming every mismatched path, such as `validation: *requests.FlightSearchRequest can't hold the input at adults, child_ages.1`, or `nil`. `dst` must be a pointer to a struct; anything else returns an error. A validator's own `v.Decode(&dst)` fills `dst` from its input and keeps the mismatches for `Validate` to report; its error is only the one for a `dst` that isn't a struct pointer.

## Manually Creating Validators

### Validating Input Without a Form Request

A request's input is validated through a form request, even a small one: a registration endpoint has its `RegisterRequest`, and the account action takes a typed `account.NewUser` its controller fills from it. When you validate something else inside a request, such as a document one field carries as JSON text, run a [validator](#the-validator-instance) over it and wrap its bag in a `*web.ValidationError` for the same 422. List the attributes in `Options.Order` in the order their errors should be reported, since a `rule.Set` is a map and has no order of its own:

```go
// airport is the document, decoded into a validation.Input.
v := validation.New(c, airport, rule.Set{
	"code":  {rule.Required, rule.String, rule.Regex(airportCode)},
	"label": {rule.RequiredWith("code"), rule.Nullable, rule.String, rule.Max(255)},
}, validation.Options{Order: []string{"code", "label"}})

passed, err := v.Validate()
if err != nil {
	return err
}

if !passed {
	return &web.ValidationError{Errors: v.Errors()}
}
```

### The Validator Instance

Outside a request, in a console command checking a file or a job checking its payload, create a validator with `validation.New`. It takes a `context.Context` (database rules query on its connection and date rules read its clock), the input as a `validation.Input` (a `map[string]any` passes as it is), the rules, and `validation.Options`. Nothing runs until you call `Validate`:

```go
v := validation.New(ctx, input, rule.Set{
	"code":  {rule.Required, rule.String, rule.Regex(airportCode)},
	"label": {rule.RequiredWith("code"), rule.Nullable, rule.String, rule.Max(255)},
}, validation.Options{Order: []string{"code", "label"}})

passed, err := v.Validate()
if err != nil {
	return fmt.Errorf("validating airport: %w", err)
}

if !passed {
	return fmt.Errorf("invalid airport: %s", v.Errors().Summary())
}
```

`Validate` returns `(false, err)` when a rule or hook couldn't run: the input is then neither valid nor invalid. Otherwise it reports whether the input passed.

> [!NOTE]
> If you decode the input yourself, use a `json.Decoder` with `UseNumber`, as the request input does. The rules accept `float64` numbers too, but [`String`](#reading-the-input) only reads a number as text when it is a `json.Number`: a `float64` reads as `""`.

The `New` function and the validator's methods:

- `New(ctx, input, rules, options)` prepares a validator.
- `Decode(dst)` fills a struct pointer from the input, best effort; what didn't fit fails `Validate` when nothing else does. See [Filling the Request](#filling-the-request).
- `Validate()` runs every rule, then every after hook, and reports whether the input passed.
- `Errors()` is the [message bag](#working-with-error-messages).
- `Validated()` is the input the rules vouched for; see [Working With Validated Input](#working-with-validated-input).
- `After(hook)` registers a hook to run once the rules have; see [Performing Additional Validation](#performing-additional-validation-1).
- `Input(key)` is the input's value at a dotted key (`"passengers.0.birth_date"`), `nil` when absent.
- `Has(keys...)` reports whether every key is present in the input, even when `null`.
- `String(key)` is the input at a key as text.
- `Array(key)` is the list at a key, with whether there is one.

The last four read the input exactly as the `validation.Input` methods of the same names do (`Input` is `Value` there), described next.

### Reading the Input

`validation.Input` is request input: a `map[string]any`, plus readers that take a dotted key and index objects and lists alike (`"passengers.0.first_name"`), the way rule attributes do. It is what the request's `c.All()` returns, so the request's own readers (`c.Input`, `c.Has`, `c.String`, ...; see [Retrieving Input](requests.md#retrieving-input)) and the validator's behave the same:

```go
input := validation.Input{
	"adults":      json.Number("2"),
	"remember":    "on",
	"middle_name": nil,
	"passengers":  []any{map[string]any{"first_name": "Ana"}},
}

input.Value("passengers.0.first_name") // "Ana"
input.Has("middle_name")               // true: present, though null
input.Filled("middle_name")            // false
input.String("adults")                 // "2"
input.Boolean("remember")              // true
input.Array("passengers")              // [map[first_name:Ana]] true
```

- `Value(key)` is the value at the key, as the input holds it, or `nil` when the key is absent.
- `Has(keys...)` reports whether every key is present, even when its value is `null`.
- `Filled(key)` reports whether the key is present and not blank: not `null`, not an empty or whitespace-only string, and not an empty list or object. A number or a boolean, `false` included, is filled.
- `String(key)` is a scalar as text: a string as it is, a `json.Number` in its text form, and `true` as `"1"`. Anything else (an absent key, `null`, `false`, a list or an object) is `""`.
- `Boolean(key)` reads a checkbox-style flag: `true` for `true`, `1`, `"1"`, `"true"`, `"on"` and `"yes"` (any case, surrounding spaces ignored), and `false` for everything else.
- `Array(key)` returns the list at the key and `true`. An object keyed `"0"` to `"n-1"`, which some clients send for a list, reads as that list in index order. Anything else, an absent key included, returns `false`.

### Ordering the Errors

The frontend shows the 422's messages, and its summary, in the order they arrive, so reef reports them in a stable order. `validation.Options.Order` ranks attribute patterns:

- Every plain attribute is reported before every attribute with a `*`, so the summary names a plain field when one failed.
- Plain attributes follow the rank of their top-level field: `departure_times.outbound` ranks with `departure_times`, even when it isn't listed itself. Within one field, the listed patterns keep their listed order and the container comes before its children.
- Wildcard attributes follow their own rank in the list, and each one's elements are reported in input order (list index order, or sorted keys for an object). A wildcard pattern that isn't listed comes after the listed ones.
- Patterns that still tie, such as plain fields nobody listed, are reported in alphabetical order, after the listed ones.

Hooks run after the rules, so a message a hook adds for an attribute that had none is reported after the rules' messages.

`web.Validate` builds the order from the form request's struct with `validation.FieldOrder`, which lists its `json` names depth first, with `*` for a list element. A field kept as sent (an `any`, a map, a type that decodes itself) is one entry, never walked into. For the booking request whose 422 is shown [above](#validation-error-response-format), it returns:

```go
validation.FieldOrder(reflect.TypeFor[requests.StoreBookingRequest]())
// [flight_id fare_class contact_email phone_code phone_number passengers passengers.*
//  passengers.*.passenger_type passengers.*.gender passengers.*.first_name
//  passengers.*.middle_name passengers.*.last_name passengers.*.birth_date
//  passengers.*.passport_number passengers.*.nationality passengers.*.issue_date
//  passengers.*.expiry_date passengers.*.issuing_country]
```

So declare a form request's fields in the order the form shows them. A validator you create takes the order as `Options.Order`.

### Customizing the Error Messages

`validation.Options` carries the same customizations a form request's methods provide:

```go
v := validation.New(ctx, input, rules, validation.Options{
	Messages:   map[string]string{"passengers.*.first_name.required": "First name is required."},
	Attributes: map[string]string{"dob": "date of birth"},
	Order:      []string{"dob", "passengers", "passengers.*", "passengers.*.first_name"},
})
```

#### Specifying a Custom Message for a Given Attribute

`Options.Messages` overrides a rule's default message for an attribute, keyed `attribute.rule` (`"contact_email.required"`), with `*` wildcards allowed (`"passengers.*.first_name.required"`). Messages are matched and substituted exactly as described in [Customizing the Error Messages](#customizing-the-error-messages).

#### Specifying Custom Attribute Values

`Options.Attributes` renames attributes in messages: `"dob"` to `"date of birth"`. See [Customizing the Validation Attributes](#customizing-the-validation-attributes).

### Performing Additional Validation

You may register hooks that run once the rules have, with `After`. Hooks run in the order you register them, and the rules described for a [form request's hook](#performing-additional-validation) apply: they run even when rules failed, they add messages for invalid input, and they return an error only when they couldn't check.

```go
v.After(func(v *validation.Validator) error {
	if v.Errors().HasAny("origin", "destination") {
		return nil
	}

	if v.String("origin") == v.String("destination") {
		v.Errors().Add("destination", "The destination must differ from the origin.")
	}

	return nil
})
```

`web.Validate` registers a form request's `After` method this way, handing it the request and `v.Errors()`. A hook's error is wrapped as `validation: after hook: ...`, and a rule's as `validation: <rule> rule on <attribute>: ...`.

### Failing Validation Yourself

Some checks aren't rules at all: a wrong password, an expired code. Return `web.ValidationFailed` to answer with the same 422 for one attribute:

```go
if !ok {
	return nil, web.ValidationFailed("email", "These credentials do not match our records.")
}
```

```json
{
    "message": "These credentials do not match our records.",
    "errors": {
        "email": ["These credentials do not match our records."]
    }
}
```

`ValidationFailed(attribute, messages...)` records each message for the attribute. For failures across several attributes, build a bag and wrap it in a `web.ValidationError` yourself:

```go
errs := &validation.Errors{}
errs.Add("departure_date", "No flights operate on this route on these dates.")
errs.Add("return_date", "No flights operate on this route on these dates.")

return nil, &web.ValidationError{Errors: errs}
```

`web.ValidationError` is the error `Validate` returns for invalid input. Its `Errors` field is the message bag; `Error()` returns the summary message; `HTTPStatus()` returns `422`; and `HTTPBody()` returns the `{"message": ..., "errors": ...}` body. The kernel finds it with `errors.As`, so you may wrap it with `%w` and it still renders as a 422 (see [Validation Errors](errors.md#validation-errors)). Like any error that knows its status, it is an expected outcome: the kernel never logs or reports it.

## Working With Validated Input

After validating with a form request, you will usually read its typed fields: `request.Origin`, `request.Adults`. Sometimes you need the input as it was sent instead. An update that writes only the keys it received has to tell an absent key from an explicit `null`, which a typed field can't. Embed `web.ValidatedInput` in the form request and read `Validated()`:

```go
// ProfileRequest validates a traveller profile (store and update).
type ProfileRequest struct {
	web.ValidatedInput
	FirstName  *string `json:"first_name"`
	MiddleName *string `json:"middle_name"`
	LastName   *string `json:"last_name"`
}
```

```go
request, err := web.Validate[requests.ProfileRequest](c)
if err != nil {
	return nil, err
}

// Only the keys the request carries, an explicit null included, each
// written to its column.
profile := models.ProfileColumns
validated := request.Validated()

values := db.Values{}
for field, column := range map[string]string{
	"first_name":  profile.FirstName,
	"middle_name": profile.MiddleName,
	"last_name":   profile.LastName,
} {
	if value, ok := validated.Get(field); ok {
		values[column] = value
	}
}
```

Map each field to its [generated column identifier](models.md#column-identifiers) rather than writing the input's keys as column names, so a key the request carries can never name a column you didn't mean to write.

`Validated()` returns an `*ordered.Object` (see [Ordered JSON Objects](helpers.md#ordered-json-objects)) holding:

- each top-level field that a ruled attribute reached, and only those: keys without rules are dropped;
- the values as sent, after the input layer's trimming, so an explicit `null` is kept and `"2"` stays a string;
- the fields in the order validation reached them: struct field order, with a list validated element by element (`child_ages.*`) after the plain fields.

For example, for a form request declaring `name`, `bio` and `child_ages` in that order, with rules for `name`, `bio`, `child_ages` and `child_ages.*`, the input `{"child_ages":[3,"2"],"bio":null,"name":"Ana","junk":1}` validates to `{"name":"Ana","bio":null,"child_ages":[3,"2"]}`.

> [!WARNING]
> Only top-level fields are filtered. A nested object or list is kept whole, including keys inside it that no rule names. Validate nested values element by element, or pick the keys you need, before storing them.

Storing `Validated()` as a JSON column (a saved search's parameters, say) keeps the request's field order as the stored key order. The embedded `ValidatedInput` is not an input field: `Decode` and `FieldOrder` skip it.

When you validate with `validation.New`, call `Validator.Validated()` for the same object.

## Working With Error Messages

The message bag is a `*validation.Errors`: messages per attribute, in the order they were added. You reach it through `v.Errors()` in a hook or a manual validator, or through the `Errors` field of a `*web.ValidationError`:

```go
var invalid *web.ValidationError
if errors.As(err, &invalid) && invalid.Errors.Has("contact_email") {
	// ...
}
```

Its zero value is an empty bag, ready to use.

#### Retrieving All Error Messages for a Field

`Get` returns an attribute's messages, in the order they were added, or `nil` when it has none. The first one is `Get(attribute)[0]`:

```go
for _, message := range v.Errors().Get("passengers.0.birth_date") {
	slog.InfoContext(ctx, "birth date rejected", "message", message)
}
```

#### Retrieving the Failing Fields

`Keys` returns the attributes that have messages, in the order they first failed, the order the 422's `errors` object lists them in. It is a copy, so changing it leaves the bag alone. Collecting a refused request's failing fields looks like this:

```go
if invalid, ok := errors.AsType[*web.ValidationError](err); ok {
	fields = append(fields, invalid.Errors.Keys()...)
}
```

#### Determining if Messages Exist for a Field

`Has` reports whether an attribute has a message, and `HasAny` whether any of several do:

```go
if errs.Has("contact_email") {
	// ...
}

if errs.HasAny("departure_date", "return_date") {
	// ...
}
```

#### Adding Messages

`Add` appends a message for an attribute. A message already recorded for that attribute is dropped, so a custom rule repeating "must be a string." after the `string` rule shows it once:

```go
errs.Add("passengers", "Expected 2 adult(s) and 0 child(ren) but received 3 and 0.")
```

#### Counting Messages

`Count` is the total number of messages across all attributes.

#### The Summary Message

`Summary` is the 422's top-level `message`: the first message, followed by "(and 1 more error)" or "(and N more errors)", or `""` for an empty bag:

```go
errs.Summary() // "The flight id field is required. (and 6 more errors)"
```

#### Encoding the Bag

`MarshalJSON` encodes the bag as an object of attribute to messages, keeping insertion order, which a Go map would lose.

## Available Validation Rules

Below is a list of all available validation rules and their function. Each rule is a value or a function in the `rule` package, and each heading below is the rule's name: the name custom message keys use (`"contact_email.email"`).

- **Booleans:** [Boolean](#boolean)
- **Strings:** [Confirmed](#confirmed), [Current Password](#current_password), [Email](#email), [Enum](#enum), [In](#in), [Max](#max), [Min](#min), [Password](#password), [Regular Expression](#regex), [Size](#size), [String](#string), [UUID](#uuid)
- **Numbers:** [Integer](#integer), [Max](#max), [Min](#min), [Numeric](#numeric), [Size](#size)
- **Arrays:** [Array](#array), [Max](#max), [Min](#min), [Size](#size)
- **Dates:** [After](#after), [After Or Equal](#after_or_equal), [Date Format](#date_format), [Timezone](#timezone)
- **Database:** [Exists](#exists), [Unique](#unique)
- **Utilities:** [Bail](#bail), [Nullable](#nullable), [Required](#required), [Required With](#required_with), [Sometimes](#sometimes)

#### after

`rule.After(other string) rule.Rule`

The field under validation must be a date after another field's date:

```go
"return_date": {rule.Required, rule.DateFormat(time.DateOnly), rule.After("departure_date")},
```

If the input has no field named `other`, it is read as a literal date instead: `"today"` (midnight UTC on the context's [clock](helpers.md#dates-and-time)), or a date in one of the formats below.

```go
"travel_date": {rule.Required, rule.After("today")},
```

The value, and the other field's value, are parsed with the attribute's own `date_format` layout when it has one, otherwise as `2006-01-02`, `2006-01-02 15:04:05` or RFC 3339; a literal date is always read in those three formats. The rule fails unless both dates parse. Message: "The return date field must be a date after departure date."

> [!NOTE]
> "Today" is the UTC day. The quickstart's flight search compares its departure date against the traveller's own day in an [after hook](#performing-additional-validation) instead, so a same-day departure west of UTC isn't refused.

#### after_or_equal

`rule.AfterOrEqual(other string) rule.Rule`

The field under validation must be a date after or equal to another field's date, or to a literal date. It reads dates exactly as [after](#after) does. Message: "The return date field must be a date after or equal to departure date."

#### array

`rule.Array` · `rule.ArrayKeys(keys ...string) rule.Rule`

The field under validation must be a list or an object. Message: "The :attribute field must be an array."

To also limit the keys an object may have, use `rule.ArrayKeys`. Every key of the object must be one you list; a list passes when each of its indexes is an allowed key, so an empty list passes too:

```go
"departure_times": {rule.Sometimes, rule.ArrayKeys("outbound", "return")},
```

Both rules are named `array`.

#### bail

`rule.Bail`

Stop running the attribute's rules after its first failure. See [Stopping on First Validation Failure](#stopping-on-first-validation-failure).

#### boolean

`rule.Boolean`

The field under validation must be able to be cast as a boolean. Accepted input are `true`, `false`, `1`, `0`, `"1"` and `"0"`; the strings `"true"` and `"false"` fail. Message: "The :attribute field must be true or false."

#### confirmed

`rule.Confirmed`

The field under validation must have a matching field of `<attribute>_confirmation`. For example, if the field under validation is `password`, a matching `password_confirmation` field must be present in the input. The two are compared strictly: `"1"` never confirms `1`. Message: "The :attribute field confirmation does not match."

#### current_password

`auth.CurrentPassword`

The field under validation must be a string matching the signed-in user's password. It lives in reef's `auth` package, since it reads the user the guard bound to the request's context (`f.Ctx`) and checks the value with [`hashing.Check`](hashing.md#verifying-that-a-password-matches-a-hash). A guest, and a user without a password (one who signed up through a provider), never pass:

```go
"current_password": {rule.Required, rule.String, auth.CurrentPassword},
```

Message: "The password is incorrect."

#### date_format

`rule.DateFormat(layout string) rule.Rule`

The field under validation must be a string in exactly the given Go time layout: it must parse, and format back to itself, so `2026-1-5` fails `time.DateOnly`:

```go
"birth_date": {rule.Required, rule.String, rule.DateFormat("02/01/2006")},
```

The message names the format in the letters the forms show: "The birth date field must match the format d/m/Y." For that reason, a layout may only combine the elements those letters spell (`2006` `01` `02` `15` `04` `05`) with separators. Anything else, a PHP spelling such as `"Y-m-d"` or a layout with letters such as `time.RFC3339` included, panics when the rule is built. `time.DateOnly` reads "Y-m-d", and `time.DateTime` reads "Y-m-d H:i:s".

The [after](#after) and [after_or_equal](#after_or_equal) rules parse the attribute's dates with its `date_format` layout.

#### email

`rule.Email`

The field under validation must be a bare email address, checked leniently against the RFC: `ana@example.com` passes, while an address with a display name, `Ana <ana@example.com>`, fails. Message: "The :attribute field must be a valid email address."

#### enum

`rule.Enum[T interface{ ~string; Enum() []string }]() rule.Rule`

The field under validation must be one of an enum's backing values. Go has no enums, so an enum is a string type that lists its cases with an `Enum() []string` method, the same method [JSON Schema](json-schema.md#enums) reads. Here is a `ProfileSex`:

```go
// ProfileSex is a traveller's sex as `profiles.sex` stores it.
type ProfileSex string

// The ProfileSex cases.
const (
	ProfileSexMale   ProfileSex = "Male"
	ProfileSexFemale ProfileSex = "Female"
	ProfileSexOther  ProfileSex = "Other"
)

// Enum is every ProfileSex, in display order.
func (ProfileSex) Enum() []string {
	return []string{string(ProfileSexMale), string(ProfileSexFemale), string(ProfileSexOther)}
}
```

Pass the type, not its cases, so a case added to the type is accepted everywhere at once:

```go
"sex": {rule.Required, rule.Enum[enums.ProfileSex]()},
```

It compares values like [in](#in), under the name `enum`. Message: "The selected sex is invalid."

#### exists

`rule.Exists[M db.Model](column string) rule.Rule`

The field under validation must exist in a database table: a row of the [model](models.md) `M`'s table, the one its `TableName` method names, must hold the value in `column`.

```go
"origin": {rule.Required, rule.String, rule.Exists[models.Airport](models.AirportColumns.Code)},
```

The query runs on the connection the context carries (see [The Connection Lives in the Context](database.md#the-connection-lives-in-the-context)), so a test's transaction sees it. Values compare as Postgres compares text: exactly, so `lhr` doesn't match `LHR`. A value no text column can hold (one with a NUL byte or invalid UTF-8) fails without querying. If the query itself fails, validation stops with its error. Message: "The selected origin is invalid."

#### in

`rule.In[T ~string | ~int](values ...T) rule.Rule`

The field under validation must be included in the given list of values. The values may be strings or integers, of any type based on either:

```go
"passengers.*.passenger_type": {rule.Required, rule.String, rule.In("Adult", "Child", "Infant")},
"w":                           {rule.Sometimes, rule.Integer, rule.In(640, 1280)},
```

The value is compared with the options as text, so the query string's `"640"` passes `rule.In(640, 1280)` just as a JSON `640` does, while `"640.0"` fails. `true` and `false` read as `"1"` and `"0"`, numbers in their text form. A list or an object never matches. An option of a named string type is compared by its value, even when the type has a `String` method that renders a label.

To pass a slice of options, spread it: `rule.In(calls.FlightTimeBands...)`. Message: "The selected :attribute is invalid."

#### integer

`rule.Integer`

The field under validation must be an integer, or a string of digits with an optional leading minus, since query strings carry numbers as text. `"1.5"` and `"+3"` fail. Message: "The :attribute field must be an integer."

`integer` also makes [max](#max), [min](#min) and [size](#size) compare the value's magnitude.

#### max

`rule.Max(n float64) rule.Rule`

The field under validation must be less than or equal to `n`. Strings, numerics and arrays are evaluated in the same fashion as the [size](#size) rule. Its messages:

- a string: "The :attribute field must not be greater than 120 characters."
- a number: "The :attribute field must not be greater than 9."
- an array: "The :attribute field must not have more than 9 items."

#### min

`rule.Min(n float64) rule.Rule`

The field under validation must be at least `n`. Strings, numerics and arrays are evaluated in the same fashion as the [size](#size) rule. Its messages:

- a string: "The :attribute field must be at least 2 characters."
- a number: "The :attribute field must be at least 0.1."
- an array: "The :attribute field must have at least 1 items."

#### nullable

`rule.Nullable`

The field under validation may be `null`: its non-implicit rules are skipped when the value is `null`. See [A Note on Optional Fields](#a-note-on-optional-fields).

#### numeric

`rule.Numeric`

The field under validation must be numeric: a number, or a string in decimal notation (`"12"`, `"-0.5"`, `"1e3"`). Hexadecimal, underscores, `Inf` and `NaN` fail. Message: "The :attribute field must be a number."

`numeric` also makes [max](#max), [min](#min) and [size](#size) compare the value's magnitude.

#### password

`rule.Password(minimum int) rule.Rule`

The field under validation must be a string of at least `minimum` characters. Message: "The :attribute field must be at least 8 characters.", or "The :attribute field must be a string." for a value that isn't one. See [Validating Passwords](#validating-passwords).

#### regex

`rule.Regex(pattern *regexp.Regexp) rule.Rule`

The field under validation must be a string that matches the given regular expression. Compile the pattern once, at package level, so a typo fails when the program starts:

```go
var phoneDigits = regexp.MustCompile(`^[\d\s]+$`)

"phone_number": {rule.Required, rule.String, rule.Max(32), rule.Regex(phoneDigits)},
```

Go's `MatchString` finds a match anywhere in the string, so anchor the pattern with `^` and `$` when the whole value must match. Message: "The :attribute field format is invalid."

#### required

`rule.Required`

The field under validation must be present in the input and not empty. A field is "empty" if it is absent, `null`, a string of only whitespace, an empty list or an empty object. `required` is implicit: it runs when the field is absent, and when it fails, the attribute's other rules don't run. Message: "The :attribute field is required."

#### required_with

`rule.RequiredWith(others ...string) rule.Rule`

The field under validation must be present and not empty only if any of the other fields is present and not empty:

```go
"label": {rule.RequiredWith("code"), rule.Nullable, rule.String, rule.Max(255)},
```

Like `required`, it is implicit. Message: "The label field is required when code is present.", with several fields joined as "code / iata is present."

#### size

`rule.Size(n float64) rule.Rule`

The field under validation must have a size matching `n`:

- For a string, `n` is its number of characters (`"EUR"` has size 3).
- For an attribute with the `numeric` or `integer` rule holding a number, `n` is its value. `max` and `min` measure the same way, so `rule.Max(9)` reads `"12"` as twelve only on such an attribute; elsewhere it is two characters.
- For a list or an object, `n` is its number of items.

Anything else is measured as text, so a number on an attribute without `numeric` or `integer` is measured by its characters. Its messages:

- a string: "The :attribute field must be 3 characters."
- a number: "The :attribute field must be 3."
- an array: "The :attribute field must contain 3 items."

#### sometimes

`rule.Sometimes`

Skip every rule of the attribute, `required` included, when it is absent from the input. See [Conditionally Adding Rules](#conditionally-adding-rules).

#### string

`rule.String`

The field under validation must be a string. Message: "The :attribute field must be a string."

#### timezone

`rule.Timezone`

The field under validation must be a canonical IANA timezone identifier, such as `Europe/London` or `UTC`. Backward-compatible aliases such as `Europe/Nicosia` fail (use `Asia/Nicosia`), so stored timezones stay canonical. The list is fixed in reef, since Go can't enumerate its zone database. Message: "The :attribute field must be a valid timezone."

#### unique

`rule.Unique[M db.Model](column string) rule.UniqueRule`

The field under validation must not exist within the given database table: no row of the model `M`'s table may hold the value in `column`. `Unique` returns a builder, a `rule.UniqueRule`; finish it with its `Rule` method:

```go
"email": {rule.Required, rule.String, rule.Email, rule.Max(255), rule.Unique[models.User](models.UserColumns.Email).Rule()},
```

**Forcing a Unique Rule to Ignore a Given ID**

When a user updates their profile, keeping their own email must not collide with themselves. Chain `Ignore` before `Rule` with a function that reads the row's primary key from the request's context, and the row whose `id` column holds it is skipped; a `nil` id skips nothing. `auth.ID` reads the signed-in user's, so the rules stay static:

```go
"email": {rule.Required, rule.String, rule.Email, rule.Max(255), rule.Unique[models.User](models.UserColumns.Email).Ignore(auth.ID).Rule()},
```

Like [exists](#exists), the query runs on the context's connection and a failed query stops validation with its error. A value no text column can hold passes without querying, since no row can hold it; the attribute's other rules are what reject it. Message: "The :attribute has already been taken."

#### uuid

`rule.UUID`

The field under validation must be a UUID of any version, in upper or lower case. Message: "The :attribute field must be a valid UUID."

## Conditionally Adding Rules

#### Validating When Present

In some situations, you may wish to run validation checks against a field only if that field is present in the input. To quickly accomplish this, add `rule.Sometimes` to the rule list:

```go
"currency": {rule.Sometimes, rule.String, rule.Size(3)},
```

In the example above, the `currency` field will only be validated if it is present in the input. Non-implicit rules already skip an absent field, so `Sometimes` matters most next to an implicit rule: `{rule.Sometimes, rule.Required, rule.String}` means "may be left out, but if sent, must not be empty". A field sent as `null` is present, so its rules run.

#### Complex Conditional Validation

A form request's rules are built from a zero value of the request, without the input, so they can't depend on it. To require a field depending on another one's value, check it in an [after hook](#performing-additional-validation). A rule that needs the request's facts, such as the signed-in user, reads them from `f.Ctx` (`auth.CurrentPassword`, `Unique(...).Ignore(auth.ID)`). When you validate with `validation.New`, you hold the input already and may build the `rule.Set` from it.

## Validating Arrays

### Validating Nested Array Input

Validating nested array input doesn't have to be a pain. You may use `*` to validate each element of a list or an object:

```go
"passengers":              {rule.Required, rule.Array, rule.Min(1), rule.Max(9)},
"passengers.*.first_name": {rule.Required, rule.String, rule.Min(2), rule.Max(100)},
"passengers.*.last_name":  {rule.Required, rule.String, rule.Min(2), rule.Max(100)},
"child_ages":              {rule.Nullable, rule.Array, rule.Max(9)},
"child_ages.*":            {rule.Integer, rule.Min(0), rule.Max(17)},
```

A wildcard expands to one attribute per element that exists: `passengers.0.first_name`, `passengers.1.first_name`, and so on. The leaf may be absent, so `required` still reports a passenger without a first name. A missing container expands to nothing, though, so give the container its own rules (`"passengers": {rule.Required, rule.Array}`) when the list itself is required.

When a rule inside a wildcard attribute names another field with a `*`, the `*` resolves to the same element. Here, each passenger's expiry date is compared with that passenger's issue date:

```go
"passengers.*.expiry_date": {rule.Required, rule.DateFormat("02/01/2006"), rule.After("passengers.*.issue_date")},
```

Give the other field a [custom attribute name](#customizing-the-validation-attributes) (`"passengers.*.issue_date": "issue date"`) so the message reads well.

### Error Messages for Array Elements

Errors for an element are keyed by the expanded attribute, `passengers.0.first_name`, and its default messages name it as it is: "The passengers.0.first_name field is required." To word them for people, use a wildcard message key or a wildcard attribute name:

```go
func (StoreBookingRequest) Messages() map[string]string {
	return map[string]string{
		"passengers.*.first_name.required": "First name is required.",
		"passengers.*.last_name.required":  "Last name is required.",
	}
}
```

A wildcard attribute name, such as `"passengers.*.last_name": "last name"` in an [`Attributes` method](#customizing-the-validation-attributes), words every rule's default message at once: "The last name field is required."

The key in `errors` stays `passengers.0.first_name`, so the frontend can place the message on the right passenger's field.

## Validating Passwords

To ensure that passwords have an adequate length, use `rule.Password` with a minimum number of characters, usually alongside `rule.Confirmed`. You might share one list for every new password:

```go
// passwordRules are the rules every new password meets: present, a string,
// at least eight characters, and confirmed.
func passwordRules() []rule.Rule {
	return []rule.Rule{rule.Required, rule.String, rule.Password(8), rule.Confirmed}
}
```

The input layer leaves `password`, `password_confirmation` and `current_password` untrimmed, so surrounding whitespace counts as part of the password, and decoding keeps it too.

To check the signed-in user's current password, use [`auth.CurrentPassword`](#current_password). A password change can run it in the same pass as the new password's rules, so one 422 reports both, and words its message in a `Messages` method:

```go
// UpdatePasswordRequest validates a signed-in password change.
type UpdatePasswordRequest struct {
	CurrentPassword string `json:"current_password"`
	Password        string `json:"password"`
}

func (UpdatePasswordRequest) Rules() rule.Set {
	return rule.Set{
		"current_password": {rule.Required, rule.String, auth.CurrentPassword},
		"password":         passwordRules(),
	}
}

func (UpdatePasswordRequest) Messages() map[string]string {
	return map[string]string{
		"current_password.current_password": "The provided password does not match your current password.",
	}
}
```

See [Hashing](hashing.md#verifying-that-a-password-matches-a-hash) for `hashing.Check`, which the rule uses.

## Custom Validation Rules

### Using Closures

When the built-in rules don't cover a check, write your own with `rule.Custom`. It takes the rule's name (for message keys), a function deciding whether the value passes, and the default message:

```go
// servedAirport requires the code of an airport with scheduled flights.
var servedAirport = rule.Custom("served_airport", func(f *rule.Field) (bool, error) {
	code, ok := f.Value.(string)
	if !ok {
		return false, nil
	}

	return models.Airports(f.Ctx).Where(models.AirportColumns.Code, code).Served().Exists()
}, "The selected :attribute is not available.")
```

```go
"origin": {rule.Required, rule.String, servedAirport},
```

The function returns an error only when it couldn't decide, such as a failed lookup, never for an invalid value. That error stops validation and the request gets a `500`. The message may use the [`:attribute` placeholders](#customizing-the-error-messages), and a request may override it under the rule's name (`"origin.served_airport"`).

A custom rule is not implicit: like the built-in ones, it is skipped when the field is absent, blank, or `null` on a nullable attribute. There are no rule objects to implement. To reuse a rule, keep it in a package-level variable, or return one, or a list of them, from a function, as `passwordRules` does above.

### Implicit Rules

By default, a rule doesn't run when the attribute is absent or blank. For a custom rule to run in those cases too, as `required` does, set its `Implicit` field:

```go
accepted := rule.Custom("accepted", func(f *rule.Field) (bool, error) {
	return f.Value == true, nil
}, "The :attribute field must be accepted.")
accepted.Implicit = true
```

> [!WARNING]
> An implicit rule sees absent and `null` values, so check `f.Present` and `f.Value` before using them. When an implicit rule fails, the attribute's remaining rules don't run.

### The Rule and Field Types

A `rule.Set` maps an attribute (dotted, with `*` wildcards allowed) to its rules:

```go
type Set map[string][]Rule
```

It is an ordinary map, so you may share and extend one. Here a request starts from a shared set of passenger rules and adds a field:

```go
rules := passengerRules()
rules["save_for_later"] = []rule.Rule{rule.Boolean}
```

A `rule.Rule` is one validation rule, a value you build with the rule package's functions and variables. Its exported fields and methods:

- `Name string` identifies the rule in custom message keys (`"required"`, `"max"`).
- `Implicit bool` makes the rule run even when the attribute is absent or empty.
- `IsMarker() bool` reports whether the rule only steers the others: true for `Sometimes`, `Nullable` and `Bail`.
- `Passes(f *Field) (bool, error)` runs the rule. An error means it couldn't run, so the value is neither valid nor invalid.
- `Message(f *Field) string` is the rule's default message, before `:attribute` is substituted.

`Passes` and `Message` let you check a rule on its own, in a unit test for example:

```go
func TestUUIDRejectsJunk(t *testing.T) {
	passed, err := rule.UUID.Passes(&rule.Field{Value: "not-a-uuid"})
	if passed || err != nil {
		t.Fatalf("passed = %v, err = %v", passed, err)
	}
}
```

A `*rule.Field` is what a rule sees:

- `Ctx context.Context` is the validator's context: the request, with its database connection and clock.
- `Attribute string` is the expanded attribute (`passengers.0.first_name`).
- `Value any` is its value, `nil` when absent.
- `Present bool` reports whether the key is in the input.
- `Input map[string]any` is the whole input, for rules that compare against other fields.
- `Rules []Rule` is the attribute's full rule list: the size rules check it for `numeric` or `integer`.
- `Display func(attribute string) string` renders another attribute's name for messages, applying custom attribute names.
- `Lookup func(attribute string) (any, bool)` reads another attribute's value from the input, resolving a `*` to the current element.
