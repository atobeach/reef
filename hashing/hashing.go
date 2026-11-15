// Package hashing hashes and checks passwords with bcrypt, in the format
// stored hashes have (`$2y$`, passwords cut at 72 bytes), so every existing
// account keeps signing in.
package hashing

import (
	"errors"
	"fmt"
	"strconv"
	"strings"

	"golang.org/x/crypto/bcrypt"
)

// maxBytes is bcrypt's input limit: stored hashes cover only the first 72
// bytes, and Go's bcrypt refuses longer input.
const maxBytes = 72

// Hasher hashes at one bcrypt cost.
type Hasher struct {
	// Rounds is the cost (BCRYPT_ROUNDS).
	Rounds int
}

// New returns a hasher with the given cost.
func New(rounds int) *Hasher {
	return &Hasher{Rounds: rounds}
}

// Make hashes value.
func (h *Hasher) Make(value string) (string, error) {
	hashed, err := bcrypt.GenerateFromPassword(truncate(value), h.Rounds)
	if err != nil {
		return "", fmt.Errorf("hashing: %w", err)
	}

	// Go writes `$2a$`, the same algorithm; stored hashes say `$2y$`.
	return "$2y$" + strings.TrimPrefix(string(hashed), "$2a$"), nil
}

// Check reports whether value matches hashed, whatever cost it was made at;
// an empty or non-bcrypt hash never matches.
func Check(value string, hashed string) bool {
	if !IsBcrypt(hashed) {
		return false
	}

	return bcrypt.CompareHashAndPassword([]byte(hashed), truncate(value)) == nil
}

// NeedsRehash reports a hash at another cost, so a sign-in upgrades it.
func (h *Hasher) NeedsRehash(hashed string) bool {
	cost, err := Cost(hashed)

	return err != nil || cost != h.Rounds
}

// IsBcrypt reports a `$2y$`/`$2a$`/`$2b$` hash of the right shape.
func IsBcrypt(hashed string) bool {
	if len(hashed) != 60 {
		return false
	}

	for _, prefix := range []string{"$2y$", "$2a$", "$2b$"} {
		if strings.HasPrefix(hashed, prefix) {
			return true
		}
	}

	return false
}

// Cost reads the work factor from a bcrypt hash.
func Cost(hashed string) (int, error) {
	if !IsBcrypt(hashed) {
		return 0, errors.New("hashing: not a bcrypt hash")
	}

	return strconv.Atoi(hashed[4:6])
}

func truncate(value string) []byte {
	if len(value) > maxBytes {
		return []byte(value[:maxBytes])
	}

	return []byte(value)
}
