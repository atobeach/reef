package support

import (
	"strings"
	"testing"
)

func TestUUID7IsAVersion7UUIDInCreationOrder(t *testing.T) {
	first := UUID7()
	if !IsUUID(first) || first[14] != '7' || (first[19] != '8' && first[19] != '9' && first[19] != 'a' && first[19] != 'b') {
		t.Fatalf("uuid7 = %s", first)
	}

	// The millisecond prefix orders ids across time.
	if later := UUID7(); later[:8] < first[:8] {
		t.Fatalf("%s sorts before %s", later, first)
	}
}

// Ids minted back to back mostly share a millisecond; they still sort in
// the order they were minted, and every one is lowercase.
func TestUUID7IsMonotonicWithinAMillisecond(t *testing.T) {
	previous := UUID7()
	for range 10_000 {
		next := UUID7()
		if next <= previous {
			t.Fatalf("%s minted after %s sorts before it", next, previous)
		}
		if strings.ToLower(next) != next {
			t.Fatalf("%s is not lowercase", next)
		}
		previous = next
	}
}

func TestIsUUIDMatchesStrIsUuid(t *testing.T) {
	for value, want := range map[string]bool{
		"01a10801-434a-7065-819d-5642ebbb8093":   true,
		"01A10801-434A-7065-819D-5642EBBB8093":   true,
		"00000000-0000-0000-0000-000000000000":   true,
		"not-a-uuid":                             false,
		"01a10801434a7065819d5642ebbb8093":       false,
		"01a10801-434a-7065-819d-5642ebbb8093\n": false,
	} {
		if got := IsUUID(value); got != want {
			t.Errorf("IsUUID(%q) = %v, want %v", value, got, want)
		}
	}
}
