package hashing

import (
	"encoding/json"
	"os"
	"strings"
	"testing"
)

type vector struct {
	Password string `json:"password"`
	Hash     string `json:"hash"`
}

func loadVectors(t *testing.T) (hashes []vector, cost12 vector) {
	t.Helper()

	raw, err := os.ReadFile("testdata/vectors.json")
	if err != nil {
		t.Fatal(err)
	}

	var v struct {
		Hashes []vector `json:"hashes"`
		Cost12 vector   `json:"cost12"`
	}
	if err := json.Unmarshal(raw, &v); err != nil {
		t.Fatal(err)
	}

	return v.Hashes, v.Cost12
}

// The vectors are $2y$ bcrypt hashes as PHP's password_hash writes them: a
// password hashed by a Laravel app must keep verifying.
func TestChecksExistingHashes(t *testing.T) {
	hashes, cost12 := loadVectors(t)

	for _, v := range append(hashes, cost12) {
		if !strings.HasPrefix(v.Hash, "$2y$") {
			t.Fatalf("vector %q lacks $2y$", v.Hash)
		}

		if !Check(v.Password, v.Hash) {
			t.Errorf("Check(%q, stored hash) = false", v.Password)
		}

		if Check(v.Password+"x", v.Hash) && len(v.Password) < maxBytes {
			t.Errorf("Check accepted a wrong password for %q", v.Password)
		}
	}
}

// Stored hashes only cover a password's first 72 bytes, so bytes past them
// must be ignored, not refused, or long passwords stop signing in.
func TestTruncatesAt72Bytes(t *testing.T) {
	h := New(4)
	hashes, _ := loadVectors(t)

	long := hashes[3]
	if !Check(strings.Repeat("a", 72)+"something else", long.Hash) {
		t.Fatal("bytes past 72 must be ignored")
	}

	made, err := h.Make(strings.Repeat("b", 100))
	if err != nil {
		t.Fatalf("a >72-byte password must hash, got %v", err)
	}

	if !Check(strings.Repeat("b", 72), made) {
		t.Fatal("truncation must apply to Make too")
	}
}

func TestMakeWritesThe2yPrefixAndCost(t *testing.T) {
	h := New(4)

	made, err := h.Make("password123")
	if err != nil {
		t.Fatal(err)
	}

	if !strings.HasPrefix(made, "$2y$04$") || len(made) != 60 {
		t.Fatalf("hash = %q", made)
	}

	if !Check("password123", made) || Check("password124", made) {
		t.Fatal("round trip failed")
	}
}

func TestNeedsRehashWhenTheCostChanged(t *testing.T) {
	_, cost12 := loadVectors(t)

	if New(4).NeedsRehash(cost12.Hash) != true || New(12).NeedsRehash(cost12.Hash) != false {
		t.Fatal("needsRehash must compare the cost with the configured rounds")
	}
}

func TestEmptyOrForeignHashesNeverMatch(t *testing.T) {
	for _, hashed := range []string{"", "password123", "$argon2id$v=19$m=65536,t=4,p=1$abc$def"} {
		if Check("password123", hashed) {
			t.Errorf("Check matched %q", hashed)
		}
	}
}
