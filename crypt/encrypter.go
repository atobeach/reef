// Package crypt seals values with APP_KEY: AES-256-CBC with an HMAC-SHA256
// over the IV and ciphertext, wrapped in base64 JSON. The format is Laravel's
// Encrypter envelope, fixed because stored cookies and secrets use it.
package crypt

import (
	"bytes"
	"crypto/aes"
	"crypto/cipher"
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
)

// ErrInvalidPayload is every way a payload can fail, alike: telling them
// apart would be a padding oracle.
var ErrInvalidPayload = errors.New("crypt: the payload is invalid")

// Encrypter encrypts with the current key and decrypts with any known key,
// so payloads sealed before a key rotation still open.
type Encrypter struct {
	key      []byte
	previous [][]byte
}

// ParseKey decodes an APP_KEY ("base64:..." or raw 32 bytes).
func ParseKey(appKey string) ([]byte, error) {
	key := []byte(appKey)
	if encoded, ok := strings.CutPrefix(appKey, "base64:"); ok {
		decoded, err := base64.StdEncoding.DecodeString(encoded)
		if err != nil {
			return nil, fmt.Errorf("crypt: APP_KEY is not valid base64: %w", err)
		}
		key = decoded
	}

	if len(key) != 32 {
		return nil, fmt.Errorf("crypt: AES-256-CBC needs a 32-byte key, APP_KEY has %d", len(key))
	}

	return key, nil
}

// New builds an encrypter from APP_KEY and APP_PREVIOUS_KEYS.
func New(appKey string, previousKeys ...string) (*Encrypter, error) {
	key, err := ParseKey(appKey)
	if err != nil {
		return nil, err
	}

	e := &Encrypter{key: key}
	for _, previous := range previousKeys {
		old, err := ParseKey(previous)
		if err != nil {
			return nil, err
		}
		e.previous = append(e.previous, old)
	}

	return e, nil
}

// Keys returns the current key first, then the previous ones.
func (e *Encrypter) Keys() [][]byte {
	return append([][]byte{e.key}, e.previous...)
}

// Key is the current key, which the cookie value prefix and other HMACs
// sign with.
func (e *Encrypter) Key() []byte {
	return e.key
}

type payload struct {
	IV    string `json:"iv"`
	Value string `json:"value"`
	MAC   string `json:"mac"`
	Tag   string `json:"tag"`
}

// EncryptString seals plaintext as is (no serialization framing).
func (e *Encrypter) EncryptString(plaintext string) (string, error) {
	iv := make([]byte, aes.BlockSize)
	if _, err := rand.Read(iv); err != nil {
		return "", fmt.Errorf("crypt: iv: %w", err)
	}

	block, err := aes.NewCipher(e.key)
	if err != nil {
		return "", fmt.Errorf("crypt: cipher: %w", err)
	}

	padded := pad([]byte(plaintext))
	ciphertext := make([]byte, len(padded))
	cipher.NewCBCEncrypter(block, iv).CryptBlocks(ciphertext, padded)

	p := payload{
		IV:    base64.StdEncoding.EncodeToString(iv),
		Value: base64.StdEncoding.EncodeToString(ciphertext),
	}
	p.MAC = mac(p.IV, p.Value, e.key)

	encoded, err := json.Marshal(p)
	if err != nil {
		return "", fmt.Errorf("crypt: envelope: %w", err)
	}

	return base64.StdEncoding.EncodeToString(encoded), nil
}

// DecryptString opens a payload sealed under any known key.
func (e *Encrypter) DecryptString(encoded string) (string, error) {
	raw, err := base64.StdEncoding.DecodeString(encoded)
	if err != nil {
		return "", ErrInvalidPayload
	}

	var p payload
	if err := json.Unmarshal(raw, &p); err != nil || p.IV == "" || p.Value == "" || p.MAC == "" {
		return "", ErrInvalidPayload
	}

	iv, err := base64.StdEncoding.DecodeString(p.IV)
	if err != nil || len(iv) != aes.BlockSize {
		return "", ErrInvalidPayload
	}

	ciphertext, err := base64.StdEncoding.DecodeString(p.Value)
	if err != nil || len(ciphertext) == 0 || len(ciphertext)%aes.BlockSize != 0 {
		return "", ErrInvalidPayload
	}

	for _, key := range e.Keys() {
		if !hmac.Equal([]byte(mac(p.IV, p.Value, key)), []byte(p.MAC)) {
			continue
		}

		block, err := aes.NewCipher(key)
		if err != nil {
			return "", ErrInvalidPayload
		}

		plaintext := make([]byte, len(ciphertext))
		cipher.NewCBCDecrypter(block, iv).CryptBlocks(plaintext, ciphertext)

		unpadded, ok := unpad(plaintext)
		if !ok {
			return "", ErrInvalidPayload
		}

		return string(unpadded), nil
	}

	return "", ErrInvalidPayload
}

func mac(iv string, value string, key []byte) string {
	h := hmac.New(sha256.New, key)
	h.Write([]byte(iv + value))

	return hex.EncodeToString(h.Sum(nil))
}

func pad(data []byte) []byte {
	n := aes.BlockSize - len(data)%aes.BlockSize

	return append(data, bytes.Repeat([]byte{byte(n)}, n)...)
}

func unpad(data []byte) ([]byte, bool) {
	n := int(data[len(data)-1])
	if n == 0 || n > aes.BlockSize || n > len(data) {
		return nil, false
	}

	for _, b := range data[len(data)-n:] {
		if int(b) != n {
			return nil, false
		}
	}

	return data[:len(data)-n], true
}
