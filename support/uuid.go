package support

import (
	"crypto/rand"
	"encoding/binary"
	"encoding/hex"
	"regexp"
	"sync"
	"time"
)

// uuidPattern is any 8-4-4-4-12 hex string, nil and max UUIDs included.
var uuidPattern = regexp.MustCompile(`^[\da-fA-F]{8}-[\da-fA-F]{4}-[\da-fA-F]{4}-[\da-fA-F]{4}-[\da-fA-F]{12}$`)

// IsUUID reports a UUID-shaped string. Postgres errors when a uuid column is
// compared to malformed text, so callers check this before querying.
func IsUUID(value string) bool {
	return uuidPattern.MatchString(value)
}

// uuid7Clock holds the last UUID7's fields, so ids within one millisecond
// count up from it. It is process-wide: monotonic ids need one sequence.
var uuid7Clock struct {
	sync.Mutex
	ms   uint64
	high uint16 // the 12 bits after the version nibble
	low  uint64 // the 62 bits after the variant bits
}

// UUID7 is a time-ordered, lowercase RFC 9562 version 7 UUID. It is
// monotonic (method 2), so ids minted within one millisecond still sort in
// order: rows keyed by them read back in insertion order.
func UUID7() string {
	ms, high, low := nextUUID7Fields()

	var id [16]byte
	var millis [8]byte
	binary.BigEndian.PutUint64(millis[:], ms)
	copy(id[:6], millis[2:])
	binary.BigEndian.PutUint16(id[6:8], 0x7000|high)           // version 7
	binary.BigEndian.PutUint64(id[8:], 0x8000000000000000|low) // RFC 4122 variant

	return formatUUID(id)
}

// nextUUID7Fields advances the clock: a new millisecond reseeds the random
// fields, the same (or an earlier) one increments the last id's.
func nextUUID7Fields() (ms uint64, high uint16, low uint64) {
	uuid7Clock.Lock()
	defer uuid7Clock.Unlock()

	ms = uint64(time.Now().UnixMilli()) //nolint:forbidigo // ids carry the real millisecond: the sequence is process-wide, never one test's clock
	if ms > uuid7Clock.ms {
		var seed [10]byte
		if _, err := rand.Read(seed[:]); err != nil {
			panic("support: no randomness: " + err.Error())
		}
		uuid7Clock.ms = ms
		uuid7Clock.high = binary.BigEndian.Uint16(seed[:2]) & 0x0fff
		// Half the range, so increments within the millisecond never wrap.
		uuid7Clock.low = binary.BigEndian.Uint64(seed[2:]) & (1<<61 - 1)

		return ms, uuid7Clock.high, uuid7Clock.low
	}

	uuid7Clock.low++
	if uuid7Clock.low >= 1<<62 {
		uuid7Clock.low = 0
		uuid7Clock.high = (uuid7Clock.high + 1) & 0x0fff
	}

	return uuid7Clock.ms, uuid7Clock.high, uuid7Clock.low
}

// UUID4 is a random RFC 4122 version 4 UUID, for ids where ordering carries
// no meaning.
func UUID4() string {
	var id [16]byte
	if _, err := rand.Read(id[:]); err != nil {
		panic("support: no randomness: " + err.Error())
	}

	id[6] = (id[6] & 0x0f) | 0x40 // version 4
	id[8] = (id[8] & 0x3f) | 0x80 // RFC 4122 variant

	return formatUUID(id)
}

func formatUUID(id [16]byte) string {
	hexed := hex.EncodeToString(id[:])

	return hexed[0:8] + "-" + hexed[8:12] + "-" + hexed[12:16] + "-" + hexed[16:20] + "-" + hexed[20:]
}
