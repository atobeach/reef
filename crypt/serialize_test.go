package crypt

import (
	"encoding/json"
	"errors"
	"os"
	"testing"
)

type serializedVectors struct {
	Key        string `json:"key"`
	Serialized []struct {
		Plain   string `json:"plain"`
		Payload string `json:"payload"`
	} `json:"serialized"`
}

func loadSerialized(t *testing.T) serializedVectors {
	t.Helper()

	raw, err := os.ReadFile("testdata/vectors.json")
	if err != nil {
		t.Fatal(err)
	}

	var v serializedVectors
	if err := json.Unmarshal(raw, &v); err != nil {
		t.Fatal(err)
	}

	if len(v.Serialized) == 0 {
		t.Fatal("vectors.json has no serialized vectors")
	}

	return v
}

// The 2FA secret and recovery-code columns hold serialized, sealed strings:
// the stored rows must keep opening.
func TestDecryptsStoredSerializedStrings(t *testing.T) {
	v := loadSerialized(t)
	e, _ := New(v.Key)

	for _, vector := range v.Serialized {
		got, err := e.Decrypt(vector.Payload)
		if err != nil || got != vector.Plain {
			t.Errorf("Decrypt = %q, %v; want %q", got, err, vector.Plain)
		}

		// DecryptString on a serialized payload exposes the framing.
		if raw, _ := e.DecryptString(vector.Payload); raw != serializeString(vector.Plain) {
			t.Errorf("plaintext = %q, want %q", raw, serializeString(vector.Plain))
		}
	}
}

// What Encrypt seals carries the same framing the stored rows do.
func TestEncryptSerializesTheStoredFraming(t *testing.T) {
	v := loadSerialized(t)
	e, _ := New(v.Key)

	for _, plain := range []string{"JBSWY3DPEHPK3PXP", "é", "", `a";s:1:"b`} {
		sealed, err := e.Encrypt(plain)
		if err != nil {
			t.Fatal(err)
		}

		if raw, _ := e.DecryptString(sealed); raw != serializeString(plain) {
			t.Errorf("plaintext = %q", raw)
		}

		if got, err := e.Decrypt(sealed); err != nil || got != plain {
			t.Errorf("round trip %q = %q, %v", plain, got, err)
		}
	}
}

func TestDecryptRejectsNonStringSerializations(t *testing.T) {
	v := loadSerialized(t)
	e, _ := New(v.Key)

	for _, plain := range []string{"plain", `i:5;`, `a:0:{}`, `s:3:"ab";`, `s:2:"abc";`, `s:-1:"";`, `s:02:"ab";`, `s:2:"ab"`} {
		sealed, _ := e.EncryptString(plain)
		if _, err := e.Decrypt(sealed); !errors.Is(err, ErrNotAString) {
			t.Errorf("Decrypt(%q) err = %v, want ErrNotAString", plain, err)
		}
	}
}
