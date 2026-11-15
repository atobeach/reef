package env

import (
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"testing"
)

func TestParsesDotenvLines(t *testing.T) {
	cases := map[string][3]string{
		"APP_ENV=local":                {"APP_ENV", "local", "ok"},
		`APP_NAME="Example Flights"`:   {"APP_NAME", "Example Flights", "ok"},
		"KEY='single quoted'":          {"KEY", "single quoted", "ok"},
		"export PORT=8000":             {"PORT", "8000", "ok"},
		"DEBUG=true # inline comment":  {"DEBUG", "true", "ok"},
		`HASH="keeps # inside quotes"`: {"HASH", "keeps # inside quotes", "ok"},
		"EMPTY=":                       {"EMPTY", "", "ok"},
		"# a comment":                  {"", "", ""},
		"not a pair":                   {"", "", ""},
	}

	for line, want := range cases {
		key, value, ok := parseLine(line)
		if want[2] == "" {
			if ok {
				t.Errorf("parseLine(%q) should be skipped", line)
			}

			continue
		}

		if !ok || key != want[0] || value != want[1] {
			t.Errorf("parseLine(%q) = %q, %q, %v", line, key, value, ok)
		}
	}
}

func TestNullStringsReadAsUnset(t *testing.T) {
	t.Setenv("REDIS_PASSWORD", "null")
	t.Setenv("APP_DEBUG", "(true)")
	t.Setenv("ORIGINS", " https://a.test , ,https://b.test")

	if got := Get("REDIS_PASSWORD", "fallback"); got != "fallback" {
		t.Errorf("Get = %q", got)
	}

	if !Bool("APP_DEBUG", false) {
		t.Error("(true) should parse as true")
	}

	if got := List("ORIGINS", ""); len(got) != 2 || got[0] != "https://a.test" || got[1] != "https://b.test" {
		t.Errorf("List = %q", got)
	}
}

func TestEnvFilesFillInOnlyWhatTheProcessDoesNotExport(t *testing.T) {
	root := t.TempDir()
	nested := filepath.Join(root, "tests", "feature")
	if err := os.MkdirAll(nested, 0o700); err != nil {
		t.Fatal(err)
	}

	write := func(name string, contents string) string {
		path := filepath.Join(root, name)
		if err := os.WriteFile(path, []byte(contents), 0o600); err != nil {
			t.Fatal(err)
		}

		return path
	}

	t.Setenv("ENV_TEST_EXPORTED", "exported")
	t.Cleanup(func() {
		_ = os.Unsetenv("ENV_TEST_FROM_DOTENV")
		_ = os.Unsetenv("ENV_TEST_FROM_TESTING")
	})

	// Load finds .env by walking up from a subdirectory.
	write(".env", "ENV_TEST_FROM_DOTENV=dotenv\nENV_TEST_EXPORTED=dotenv\n")
	if err := Load(nested); err != nil {
		t.Fatal(err)
	}

	envTesting := write(".env.testing", "ENV_TEST_FROM_TESTING=testing\nENV_TEST_EXPORTED=testing\n")
	if err := LoadFile(envTesting); err != nil {
		t.Fatal(err)
	}

	for key, want := range map[string]string{"ENV_TEST_FROM_DOTENV": "dotenv", "ENV_TEST_FROM_TESTING": "testing", "ENV_TEST_EXPORTED": "exported"} {
		if got := Get(key, ""); got != want {
			t.Errorf("%s = %q, want %q", key, got, want)
		}
	}

	if err := LoadFile(filepath.Join(root, ".env.missing")); !errors.Is(err, fs.ErrNotExist) {
		t.Errorf("a missing file named outright must fail, got %v", err)
	}
}
