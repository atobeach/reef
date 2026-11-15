package session

import (
	"encoding/json"
	"testing"
	"time"
)

// Sessions already stored hold `login.id` and `auth.password_confirmed_at`
// nested in the payload; reads and writes must keep that shape.
func TestDottedKeysNest(t *testing.T) {
	const (
		loginID       Key[int64] = "login.id"
		loginRemember Key[bool]  = "login.remember"
	)

	s := newSession()
	loginID.Put(s, 7)
	loginRemember.Put(s, true)
	s.PasswordConfirmed(time.Unix(1_700_000_000, 0))

	raw, _ := json.Marshal(s.All())
	var payload map[string]any
	_ = json.Unmarshal(raw, &payload)

	login, _ := payload["login"].(map[string]any)
	if login["id"] != float64(7) || login["remember"] != true {
		t.Fatalf("login = %v", payload["login"])
	}

	if auth, _ := payload["auth"].(map[string]any); auth["password_confirmed_at"] != float64(1_700_000_000) {
		t.Fatalf("auth = %v", payload["auth"])
	}

	if id, ok := loginID.Get(s); id != 7 || !ok {
		t.Fatalf("login.id = %d, %v; dotted reads must walk the nesting", id, ok)
	}

	if remember, ok := loginRemember.Pull(s); !remember || !ok {
		t.Fatalf("Pull = %v, %v; want the stored true", remember, ok)
	}

	if _, ok := loginRemember.Get(s); ok {
		t.Fatal("Pull must remove the leaf")
	}

	if _, ok := loginID.Get(s); !ok {
		t.Fatal("Pull must remove only the leaf")
	}

	loginID.Forget(s)
	if _, ok := loginID.Get(s); ok {
		t.Fatal("Forget must remove the dotted leaf")
	}
}

func TestAnExactTopLevelKeyWinsOverTheDottedPath(t *testing.T) {
	attributes := map[string]any{"a.b": "flat", "a": map[string]any{"b": "nested"}}

	if getDotted(attributes, "a.b") != "flat" {
		t.Fatal("a read prefers the literal key")
	}

	forgetDotted(attributes, "a.b")
	if _, ok := attributes["a.b"]; ok || getDotted(attributes, "a.b") != "nested" {
		t.Fatal("forget removes the literal key first")
	}
}

func TestRegenerateWithTokenCyclesBoth(t *testing.T) {
	s := newSession()
	id, token := s.ID(), s.Token()

	s.RegenerateWithToken(false)

	if s.ID() == id || s.Token() == token || len(s.Token()) != 40 {
		t.Fatal("regenerate must mint a new id and CSRF token")
	}
}

// A test's browser seeds and reads stored rows through Decode and Encode, so
// they must round-trip the stored shape, numbers included.
func TestDecodeReadsWhatEncodeStores(t *testing.T) {
	s := newSession()
	Key[int64]("login.id").Put(s, 7)

	payload, err := s.Encode()
	if err != nil {
		t.Fatal(err)
	}

	decoded, err := Decode(s.ID(), payload)
	if err != nil {
		t.Fatal(err)
	}

	if stored, _ := Key[any]("login.id").Get(decoded); decoded.ID() != s.ID() || stored != json.Number("7") || decoded.Token() != s.Token() {
		t.Fatalf("decoded %s = %v", decoded.ID(), decoded.All())
	}

	if empty, err := Decode("fresh", ""); err != nil || len(empty.All()) != 0 {
		t.Fatalf("an empty payload = %v, %v", empty, err)
	}

	if _, err := Decode("broken", "not base64!"); err == nil {
		t.Fatal("a corrupt payload must fail")
	}
}

// A stored integer reads back in whichever form the session holds it: put
// this request, decoded from the store, or a numeric string. The password
// confirmation reads it as a unix time; absent or unreadable is the zero
// time.
func TestAnInt64KeyReadsAStoredNumberInAnyForm(t *testing.T) {
	for _, tt := range []struct {
		name  string
		value any
		want  int64
		ok    bool
	}{
		{"an int64 put this request", int64(1_700_000_000), 1_700_000_000, true},
		{"an int put this request", 1_700_000_000, 1_700_000_000, true},
		{"a float64 put this request", float64(1_700_000_000), 1_700_000_000, true},
		{"a json.Number decoded from the store", json.Number("1700000000"), 1_700_000_000, true},
		{"a json.Number in float form", json.Number("1.7e9"), 1_700_000_000, true},
		{"a numeric string", "1700000000", 1_700_000_000, true},
		{"a string that is no number", "soon", 0, false},
		{"a value that is no number", true, 0, false},
		{"an absent key", nil, 0, false},
	} {
		t.Run(tt.name, func(t *testing.T) {
			s := newSession()
			if tt.value != nil {
				Key[any](passwordConfirmedAt).Put(s, tt.value)
			}

			if got, ok := passwordConfirmedAt.Get(s); got != tt.want || ok != tt.ok {
				t.Fatalf("Get = %d, %v; want %d, %v", got, ok, tt.want, tt.ok)
			}

			want := time.Time{}
			if tt.ok {
				want = time.Unix(tt.want, 0).UTC()
			}

			if got := s.PasswordConfirmedAt(); got != want {
				t.Fatalf("PasswordConfirmedAt = %v, want %v", got, want)
			}
		})
	}
}

// A value that isn't a T reads as absent, never as T's zero value with ok.
func TestAKeyReadsAValueOfAnotherTypeAsAbsent(t *testing.T) {
	s := newSession()
	Key[string]("remember").Put(s, "yes")
	Key[int64]("count").Put(s, 3)

	if remember, ok := Key[bool]("remember").Get(s); remember || ok {
		t.Fatalf(`a bool read of "yes" = %v, %v; want absent`, remember, ok)
	}

	if count, ok := Key[string]("count").Get(s); count != "" || ok {
		t.Fatalf("a string read of 3 = %q, %v; want absent", count, ok)
	}
}

type pending struct {
	Email    string `json:"email"`
	Attempts int    `json:"attempts"`
}

// A struct put this request and the same struct decoded from the store
// (a map of json.Numbers by then) read the same.
func TestAStructKeyReadsTheSameBeforeAndAfterTheStore(t *testing.T) {
	const key Key[pending] = "pending"
	want := pending{Email: "a@b.co", Attempts: 2}

	s := newSession()
	key.Put(s, want)

	payload, err := s.Encode()
	if err != nil {
		t.Fatal(err)
	}

	stored, err := Decode(s.ID(), payload)
	if err != nil {
		t.Fatal(err)
	}

	for name, session := range map[string]*Session{"this request": s, "the store": stored} {
		if got, ok := key.Get(session); got != want || !ok {
			t.Fatalf("from %s: Get = %+v, %v; want %+v", name, got, ok, want)
		}
	}
}

// A flashed value survives the save that follows (the next request reads
// it), then goes; Reflash keeps it for one more.
func TestAFlashedValueLastsOneRequest(t *testing.T) {
	const status Key[string] = "status"

	for _, tt := range []struct {
		name    string
		reflash bool
		saves   int
		want    bool
	}{
		{"on the next request", false, 1, true},
		{"on the one after", false, 2, false},
		{"reflashed, on the one after", true, 2, true},
	} {
		t.Run(tt.name, func(t *testing.T) {
			s := newSession()
			status.Flash(s, "saved")

			for save := range tt.saves {
				s.ageFlash()
				if tt.reflash && save == 0 {
					s.Reflash()
				}
			}

			if got, ok := status.Get(s); ok != tt.want || (ok && got != "saved") {
				t.Fatalf("Get = %q, %v; want present: %v", got, ok, tt.want)
			}
		})
	}
}

// PasswordConfirmedAt reads the stamp PasswordConfirmed wrote, on this
// request and once the session went through the store.
func TestPasswordConfirmedAtReadsTheStamp(t *testing.T) {
	s := newSession()
	if !s.PasswordConfirmedAt().IsZero() {
		t.Fatalf("never confirmed = %v, want the zero time", s.PasswordConfirmedAt())
	}

	at := time.Unix(1_700_000_000, 0)
	s.PasswordConfirmed(at)

	payload, err := s.Encode()
	if err != nil {
		t.Fatal(err)
	}

	stored, err := Decode(s.ID(), payload)
	if err != nil {
		t.Fatal(err)
	}

	if !s.PasswordConfirmedAt().Equal(at) || !stored.PasswordConfirmedAt().Equal(at) {
		t.Fatalf("confirmed at = %v (stored: %v), want %v", s.PasswordConfirmedAt(), stored.PasswordConfirmedAt(), at)
	}
}
