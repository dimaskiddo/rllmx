package opencode

import (
	"crypto/sha256"
	"strconv"
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
