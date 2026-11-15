package crypt

import (
	"errors"
	"strconv"
	"strings"
)

// ErrNotAString is a payload that decrypts to something other than a
// serialized string (`s:<len>:"…";`), which nothing here guesses at.
var ErrNotAString = errors.New("crypt: the payload is not a serialized string")

// Encrypt seals value framed as a serialized string (`s:16:"…";`), the
// framing values already stored carry.
func (e *Encrypter) Encrypt(value string) (string, error) {
	return e.EncryptString(serializeString(value))
}

// Decrypt opens a payload Encrypt sealed.
func (e *Encrypter) Decrypt(payload string) (string, error) {
	plain, err := e.DecryptString(payload)
	if err != nil {
		return "", err
	}

	return unserializeString(plain)
}

// serializeString frames value; the length is in bytes.
func serializeString(value string) string {
	return "s:" + strconv.Itoa(len(value)) + `:"` + value + `";`
}

// unserializeString accepts exactly what serializeString produces, checking
// the declared byte length against the body.
func unserializeString(plain string) (string, error) {
	rest, ok := strings.CutPrefix(plain, "s:")
	if !ok {
		return "", ErrNotAString
	}

	digits, rest, ok := strings.Cut(rest, ":")
	if !ok {
		return "", ErrNotAString
	}

	n, err := strconv.Atoi(digits)
	if err != nil || n < 0 || strconv.Itoa(n) != digits || len(rest) != n+3 {
		return "", ErrNotAString
	}

	if rest[0] != '"' || rest[n+1:] != `";` {
		return "", ErrNotAString
	}

	return rest[1 : n+1], nil
}
