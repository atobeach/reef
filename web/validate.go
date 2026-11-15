package web

import (
	"errors"
	"fmt"
	"net/http"
	"reflect"
	"sync"

	"github.com/atobeach/reef/ordered"
	"github.com/atobeach/reef/validation"
	"github.com/atobeach/reef/validation/rule"
)

var errNotStruct = errors.New("web: a form request must be a struct type")

// FormRequest is a validated request type: a struct whose `json` tags name
// the input fields, plus its rules. Every rule names a field and every field
// has a rule (CheckFormRequest). Optional methods customise validation:
//
//	Messages() map[string]string                     // custom messages (value receiver)
//	Attributes() map[string]string                   // custom attribute names (value receiver)
//	After(c *Context, errs *validation.Errors) error // checks on the decoded request
//
// Embedding ValidatedInput adds req.Validated().
type FormRequest interface {
	Rules() rule.Set
}

// ValidatedInput adds req.Validated() to a FormRequest that embeds it. A
// typed field can't tell an absent key from an explicit null; a partial
// update and a stored payload need the present keys, valued as sent.
type ValidatedInput struct {
	validated *ordered.Object
}

// Validated is the input that passed validation: present keys only, valued
// as sent, ordered as validation.Validator.Validated orders them.
func (in *ValidatedInput) Validated() *ordered.Object {
	return in.validated
}

func (in *ValidatedInput) setValidated(validated *ordered.Object) {
	in.validated = validated
}

type withMessages interface {
	Messages() map[string]string
}

type withAttributes interface {
	Attributes() map[string]string
}

type withAfter interface {
	After(c *Context, errs *validation.Errors) error
}

// Validate decodes the request input into T and validates it against T's
// rules. A value T can't hold fails as the rule its field's type implies; an
// After hook reads the decoded T, even when the rules failed. A failure is a
// *ValidationError, which renders as a 422; a rule that couldn't run (the
// database is down) returns its error, a 500, and so does a T that
// CheckFormRequest refuses.
//
//	req, err := web.Validate[requests.StoreBooking](c)
//	if err != nil {
//		return nil, err
//	}
func Validate[T FormRequest](c *Context) (T, error) {
	var request T

	p := planFor[T]()
	if p.err != nil {
		return request, p.err
	}

	input, err := c.All()
	if err != nil {
		return request, err
	}

	options := validation.Options{Order: p.order}
	if m, ok := any(request).(withMessages); ok {
		options.Messages = m.Messages()
	}

	if a, ok := any(request).(withAttributes); ok {
		options.Attributes = a.Attributes()
	}

	validator := validation.New(c, input, request.Rules(), options)
	if err := validator.Decode(&request); err != nil {
		return request, err
	}

	// Through the pointer, so a value receiver and a pointer one both see
	// the decoded request.
	if hooked, ok := any(&request).(withAfter); ok {
		validator.After(func(v *validation.Validator) error { return hooked.After(c, v.Errors()) })
	}

	if err := runValidator(validator); err != nil {
		return request, err
	}

	if embedded, ok := any(&request).(interface{ setValidated(*ordered.Object) }); ok {
		embedded.setValidated(validator.Validated())
	}

	return request, nil
}

// plans holds each form request type's plan, built on its first Validate.
// Like encoding/json's field cache, it holds only what a type determines,
// which never changes.
var plans sync.Map // reflect.Type → plan

// plan is what Validate derives from a form request's type: the order its
// errors report in, or why the type can't be validated.
type plan struct {
	order []string
	err   error
}

func planFor[T FormRequest]() plan {
	t := reflect.TypeFor[T]()
	if cached, ok := plans.Load(t); ok {
		return cached.(plan)
	}

	p := plan{err: CheckFormRequest[T]()}
	if p.err == nil {
		p.order = validation.FieldOrder(t)
	}
	plans.Store(t, p)

	return p
}

// CheckFormRequest reports what keeps T from validating as declared: T isn't
// a struct, T's rules and fields disagree (validation.CheckFields), or an
// optional method has the right name but not the signature Validate calls,
// so it would silently never run. Validate refuses such a T on every call;
// a test may check every form request at once.
func CheckFormRequest[T FormRequest]() error {
	t := reflect.TypeFor[T]()
	if t.Kind() != reflect.Struct {
		return errNotStruct
	}

	var zero T

	return errors.Join(validation.CheckFields(t, zero.Rules()), checkHooks(t))
}

// checkHooks refuses a Messages, Attributes or After method Validate can't
// call: Messages and Attributes are read off a value, After through a
// pointer.
func checkHooks(t reflect.Type) error {
	hooks := []struct {
		name      string
		receiver  reflect.Type
		satisfies reflect.Type
		signature string
	}{
		{"Messages", t, reflect.TypeFor[withMessages](), "Messages() map[string]string, on a value receiver"},
		{"Attributes", t, reflect.TypeFor[withAttributes](), "Attributes() map[string]string, on a value receiver"},
		{"After", reflect.PointerTo(t), reflect.TypeFor[withAfter](), "After(c *web.Context, errs *validation.Errors) error"},
	}

	var problems []error
	for _, hook := range hooks {
		if _, declared := reflect.PointerTo(t).MethodByName(hook.name); declared && !hook.receiver.Implements(hook.satisfies) {
			problems = append(problems, fmt.Errorf("web: %s's %s must be %s", t, hook.name, hook.signature))
		}
	}

	return errors.Join(problems...)
}

// runValidator turns invalid input into the 422; an error means a rule
// couldn't run (the database is down), for the kernel's 500.
func runValidator(validator *validation.Validator) error {
	passed, err := validator.Validate()
	if err != nil {
		return err
	}

	if !passed {
		return &ValidationError{Errors: validator.Errors()}
	}

	return nil
}

// ValidationFailed is a 422 for one attribute, for checks no rule expresses
// (wrong password, an expired code).
func ValidationFailed(attribute string, messages ...string) *ValidationError {
	errs := &validation.Errors{}
	for _, message := range messages {
		errs.Add(attribute, message)
	}

	return &ValidationError{Errors: errs}
}

// ValidationError is a 422 with the summary message and every attribute's
// messages.
type ValidationError struct {
	Errors *validation.Errors
}

func (e *ValidationError) Error() string {
	return e.Errors.Summary()
}

// HTTPStatus implements the kernel's status contract.
func (e *ValidationError) HTTPStatus() int {
	return http.StatusUnprocessableEntity
}

// HTTPBody is the JSON body: {"message": ..., "errors": {...}}.
func (e *ValidationError) HTTPBody() any {
	return struct {
		Message string             `json:"message"`
		Errors  *validation.Errors `json:"errors"`
	}{e.Errors.Summary(), e.Errors}
}
