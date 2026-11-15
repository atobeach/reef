// Package cookie is the encrypted cookies' value format: the plaintext is
// prefixed with an HMAC of the cookie's name, so a value can't be replayed
// under another name. Browsers hold cookies sealed this way, so it is fixed.
package cookie

import (
	"crypto/hmac"
	"crypto/sha1"
	"encoding/hex"
	"strings"

	"github.com/atobeach/reef/crypt"
)

// Prefix is hex(HMAC-SHA1(name + "v2", key)) + "|".
func Prefix(name string, key []byte) string {
	h := hmac.New(sha1.New, key)
	h.Write([]byte(name + "v2"))

	return hex.EncodeToString(h.Sum(nil)) + "|"
}

// Encrypt seals a cookie value for name.
func Encrypt(encrypter *crypt.Encrypter, name string, value string) (string, error) {
	return encrypter.EncryptString(Prefix(name, encrypter.Key()) + value)
}

// Decrypt opens an incoming value; ok is false for anything that isn't a
// valid payload for this cookie name, which the caller treats as absent.
func Decrypt(encrypter *crypt.Encrypter, name string, value string) (string, bool) {
	plain, err := encrypter.DecryptString(value)
	if err != nil {
		return "", false
	}

	for _, key := range encrypter.Keys() {
		if rest, ok := strings.CutPrefix(plain, Prefix(name, key)); ok {
			return rest, true
		}
	}

	return "", false
}
