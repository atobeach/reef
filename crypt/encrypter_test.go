package crypt

import (
	"encoding/base64"
	"encoding/json"
	"errors"
	"os"
	"strings"
	"testing"
)

type vectors struct {
	Key         string `json:"key"`
	PreviousKey string `json:"previous_key"`
	Strings     []struct {
		Plain   string `json:"plain"`
		Payload string `json:"payload"`
	} `json:"strings"`
	Previous struct {
		Plain   string `json:"plain"`
		Payload string `json:"payload"`
	} `json:"previous"`
}

func load(t *testing.T) vectors {
	t.Helper()

	raw, err := os.ReadFile("testdata/vectors.json")
	if err != nil {
		t.Fatal(err)
	}

	var v vectors
	if err := json.Unmarshal(raw, &v); err != nil {
		t.Fatal(err)
	}

	return v
}

func TestDecryptsStoredPayloads(t *testing.T) {
	v := load(t)
	e, err := New(v.Key)
	if err != nil {
		t.Fatal(err)
	}

	for _, vector := range v.Strings {
		got, err := e.DecryptString(vector.Payload)
		if err != nil || got != vector.Plain {
			t.Errorf("decrypt(%q...) = %q, %v; want %q", vector.Payload[:16], got, err, vector.Plain)
		}
	}
}

func TestDecryptsWithAPreviousKeyAfterRotation(t *testing.T) {
	v := load(t)

	current, _ := New(v.Key)
	if _, err := current.DecryptString(v.Previous.Payload); err == nil {
		t.Fatal("a payload under an unknown key must not decrypt")
	}

	rotated, err := New(v.Key, v.PreviousKey)
	if err != nil {
		t.Fatal(err)
	}

	if got, err := rotated.DecryptString(v.Previous.Payload); err != nil || got != v.Previous.Plain {
		t.Fatalf("got %q, %v", got, err)
	}
}

func TestRejectsTamperedPayloads(t *testing.T) {
	v := load(t)
	e, _ := New(v.Key)

	raw, _ := base64.StdEncoding.DecodeString(v.Strings[1].Payload)
	var p payload
	_ = json.Unmarshal(raw, &p)

	// Flip one ciphertext byte: the MAC no longer matches.
	value, _ := base64.StdEncoding.DecodeString(p.Value)
	value[0] ^= 1
	p.Value = base64.StdEncoding.EncodeToString(value)
	forged, _ := json.Marshal(p)

	for _, bad := range []string{base64.StdEncoding.EncodeToString(forged), "not base64!", base64.StdEncoding.EncodeToString([]byte(`{"iv":"","value":"","mac":""}`))} {
		if _, err := e.DecryptString(bad); !errors.Is(err, ErrInvalidPayload) {
			t.Errorf("DecryptString(%.20q) err = %v, want ErrInvalidPayload", bad, err)
		}
	}
}

func TestEncryptsInTheStoredEnvelope(t *testing.T) {
	v := load(t)
	e, _ := New(v.Key)

	sealed, err := e.EncryptString("P9qWY7Qtc41V1DswADWx8SVMV04sQE38ZwGSBUyY")
	if err != nil {
		t.Fatal(err)
	}

	raw, _ := base64.StdEncoding.DecodeString(sealed)
	if !strings.HasPrefix(string(raw), `{"iv":"`) || !strings.HasSuffix(string(raw), `","tag":""}`) || strings.Contains(string(raw), `\/`) {
		t.Fatalf("envelope = %s", raw)
	}

	if got, err := e.DecryptString(sealed); err != nil || got != "P9qWY7Qtc41V1DswADWx8SVMV04sQE38ZwGSBUyY" {
		t.Fatalf("round trip = %q, %v", got, err)
	}
}

func TestKeysMustBe32Bytes(t *testing.T) {
	for _, key := range []string{"", "base64:c2hvcnQ=", "base64:%%%"} {
		if _, err := New(key); err == nil {
			t.Errorf("New(%q) should fail", key)
		}
	}
}
