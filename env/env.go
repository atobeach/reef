// Package env loads the environment: a `.env` file fills in values, and
// anything already exported wins.
package env

import (
	"bufio"
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"strconv"
	"strings"
)

// Load reads the first `.env` in dir or a parent, so a command run from a
// subdirectory finds it. A missing file is fine: production exports
// everything.
func Load(dir string) error {
	path, err := findUp(dir, ".env")
	if errors.Is(err, fs.ErrNotExist) {
		return nil
	}

	if err != nil {
		return err
	}

	return LoadFile(path)
}

// LoadFile reads the env file at path (`.env.testing`, say); unlike Load, a
// missing file is an error.
func LoadFile(path string) error {
	file, err := os.Open(path)
	if err != nil {
		return err
	}
	defer func() { _ = file.Close() }()

	scanner := bufio.NewScanner(file)
	for scanner.Scan() {
		key, value, ok := parseLine(scanner.Text())
		if !ok {
			continue
		}

		if _, exists := os.LookupEnv(key); !exists {
			if err := os.Setenv(key, value); err != nil {
				return err
			}
		}
	}

	return scanner.Err()
}

// Get returns the variable, or fallback when it is unset, empty, "null" or
// "(null)": existing `.env` files write `REDIS_PASSWORD=null`.
func Get(key string, fallback string) string {
	value, ok := os.LookupEnv(key)
	if !ok || value == "" || value == "null" || value == "(null)" {
		return fallback
	}

	return value
}

// Bool parses the spellings existing `.env` files use ("true", "(true)",
// "1", "yes", "on", and their negatives).
func Bool(key string, fallback bool) bool {
	switch strings.ToLower(strings.Trim(Get(key, ""), "()")) {
	case "true", "1", "yes", "on":
		return true
	case "false", "0", "no", "off", "empty":
		return false
	default:
		return fallback
	}
}

// Int returns the variable as an int, or fallback when unset or malformed.
func Int(key string, fallback int) int {
	value, err := strconv.Atoi(Get(key, ""))
	if err != nil {
		return fallback
	}

	return value
}

// List splits a comma-separated variable, trimming and dropping blanks.
func List(key string, fallback string) []string {
	var list []string
	for item := range strings.SplitSeq(Get(key, fallback), ",") {
		if item = strings.TrimSpace(item); item != "" {
			list = append(list, item)
		}
	}

	return list
}

func parseLine(line string) (key string, value string, ok bool) {
	line = strings.TrimSpace(line)
	if line == "" || strings.HasPrefix(line, "#") {
		return "", "", false
	}

	line = strings.TrimPrefix(line, "export ")
	key, value, ok = strings.Cut(line, "=")
	if !ok {
		return "", "", false
	}

	key = strings.TrimSpace(key)
	value = strings.TrimSpace(value)

	if len(value) >= 2 && (value[0] == '"' || value[0] == '\'') && value[len(value)-1] == value[0] {
		value = value[1 : len(value)-1]
	} else if hash := strings.Index(value, " #"); hash >= 0 {
		value = strings.TrimSpace(value[:hash])
	}

	return key, value, key != ""
}

func findUp(dir string, name string) (string, error) {
	dir, err := filepath.Abs(dir)
	if err != nil {
		return "", err
	}

	for {
		candidate := filepath.Join(dir, name)
		if _, err := os.Stat(candidate); err == nil {
			return candidate, nil
		}

		parent := filepath.Dir(dir)
		if parent == dir {
			return "", fs.ErrNotExist
		}
		dir = parent
	}
}
