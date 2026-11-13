package opencode

import (
	"crypto/sha256"
	"strconv"
	"sync"
	"time"
)

// The opencode session id's shape: "ses_", twelve hex of a descending time
// field, then fourteen base-62 characters. The upstream decodes the field and
// refuses a session whose counter is not small, so it must keep the real shape.
const (
	opencodeBase62     = "0123456789ABCDEFGHIJKLMNOPQRSTUVWXYZabcdefghijklmnopqrstuvwxyz"
	opencodeTimeBits   = 1 << 36
	opencodeTimeHex    = 12
	opencodeRandomPart = 14
)

// opencodeSessionID derives a session id from the keyed conversation. The time
// field comes from the digest rather than the clock, so one conversation keeps
// one id across restarts; the counter is the one the client itself sends.
func opencodeSessionID(seed string) string {
	sum := sha256.Sum256([]byte(seed))
	return opencodeSessionFromDigest(sum[:])
}

// opencodeSessionFromDigest builds the id the way the reference does: five
// digest bytes masked to the time field, the counter pinned to one, and the
// following bytes taken into the base-62 alphabet.
func opencodeSessionFromDigest(d []byte) string {
	time := beUint40(d[:5]) & (opencodeTimeBits - 1)
	field := descendTime(time, 1)
	var b []byte
	b = append(b, "ses_"...)
	b = append(b, field...)
	for i := 6; i < 20 && i < len(d); i++ {
		b = append(b, opencodeBase62[int(d[i])%62])
	}
	for len(b) < len("ses_")+opencodeTimeHex+opencodeRandomPart {
		b = append(b, '0')
	}
	return string(b)
}

// descendTime is the time field: the low 48 bits of the complement of
// time<<12 plus the counter, in twelve lower-case hex digits.
func descendTime(time uint64, counter uint64) string {
	v := ^((time << 12) + counter)
	v &= (1 << 48) - 1
	h := strconv.FormatUint(v, 16)
	for len(h) < opencodeTimeHex {
		h = "0" + h
	}
	return h
}

func beUint40(b []byte) uint64 {
	var v uint64
	for _, c := range b {
		v = v<<8 | uint64(c)
	}
	return v
}

// opencodeRequestID is the request id a free-lane call carries: the same shape
// under "msg_", from the clock, because the upstream reads it as "now".
func opencodeRequestID(now int64) string {
	value := uint64(now)*0x1000 + 1
	var b []byte
	b = append(b, "msg_"...)
	for i := 5; i >= 0; i-- {
		b = append(b, hexDigit(byte(value>>(8*uint(i)))&0xff)...)
	}
	for i := 0; i < opencodeRandomPart; i++ {
		b = append(b, '0')
	}
	return string(b)
}

func hexDigit(v byte) string {
	const digits = "0123456789abcdef"
	return string([]byte{digits[v>>4], digits[v&0xf]})
}

// sessionGens remembers a conversation's session generation after the upstream
// rejected an id. The map is bounded and entries expire, so a long run cannot
// grow it: one conversation keeps one id until the lane refuses it.
type sessionGens struct {
	mu      sync.Mutex
	entries map[string]sessionGen
}

type sessionGen struct {
	gen uint32
	at  time.Time
}

const (
	genSeedsMax = 4096
	genTTL      = 24 * time.Hour
)

func (s *sessionGens) get(seed string, now time.Time) uint32 {
	s.mu.Lock()
	defer s.mu.Unlock()
	e, ok := s.entries[seed]
	if !ok || now.Sub(e.at) > genTTL {
		return 0
	}
	return e.gen
}

func (s *sessionGens) bump(seed string, now time.Time) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.entries == nil {
		s.entries = make(map[string]sessionGen, 16)
	}
	if len(s.entries) >= genSeedsMax {
		s.expire(now)
	}
	s.entries[seed] = sessionGen{gen: s.entries[seed].gen + 1, at: now}
}

// expire drops entries past their life; a map still full afterwards is cleared
// rather than grown, which only costs a conversation its earlier generation.
func (s *sessionGens) expire(now time.Time) {
	for k, e := range s.entries {
		if now.Sub(e.at) > genTTL {
			delete(s.entries, k)
		}
	}
	if len(s.entries) >= genSeedsMax {
		s.entries = make(map[string]sessionGen, 16)
	}
}

// seedWithGen mixes the generation into the seed, so a bumped conversation
// derives a different id.
func seedWithGen(seed string, gen uint32) string {
	if gen == 0 {
		return seed
	}
	return seed + "\x00gen:" + strconv.FormatUint(uint64(gen), 10)
}
