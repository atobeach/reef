package validation

import (
	"slices"
	"testing"
)

// Keys are the failing attributes in the order they failed, each once,
// and changing what Keys returned leaves the bag as it was.
func TestKeysAreTheFailingAttributesInInsertionOrder(t *testing.T) {
	var errs Errors
	errs.Add("email", "The email field is required.")
	errs.Add("password", "The password field is required.")
	errs.Add("email", "The email field must be a valid email address.")

	keys := errs.Keys()
	if want := []string{"email", "password"}; !slices.Equal(keys, want) {
		t.Fatalf("Keys() = %q, want %q", keys, want)
	}

	keys[0] = "changed"
	if got := errs.Keys(); got[0] != "email" {
		t.Fatalf("changing the returned keys reached the bag: %q", got)
	}
}
