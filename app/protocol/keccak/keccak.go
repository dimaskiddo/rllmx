// Package keccak runs the Keccak-f[1600] permutation with a chosen first
// round, which is what DeepSeek's proof of work asks for: SHA3-256's padding
// over every round except the first. Neither crypto/sha3 nor golang.org/x/crypto
// exposes a partial-round hash, and a signed protocol is our own code (like
// sigv4 and cosy), so the permutation lives here and imports nothing.
package keccak

// Rounds of the permutation. A hash with first = 0 is FIPS 202 SHA3-256; with
// first = 1 it is DeepSeekHashV1.
const (
	rounds = 24
	rate   = 136 // SHA3-256's rate: 1088 bits.
	lanes  = 25
)

// rotationOffsets[i] is the ρ rotation of lane i, from FIPS 202 §3.2.2: lane
// (x, y) is rotated by ((t+1)(t+2)/2) mod 64 where t counts along the π chain
// starting at lane 1. Filled at init from that recurrence.
var rotationOffsets [lanes]uint64

// roundConstants are the 24 ι constants of FIPS 202 §3.2.5. They come from an
// 8-bit LFSR whose bits land at positions 2^j − 1, so they are not a byte
// sequence and every implementation publishes the table.
var roundConstants = [rounds]uint64{
	0x0000000000000001, 0x0000000000008082, 0x800000000000808A, 0x8000000080008000,
	0x000000000000808B, 0x0000000080000001, 0x8000000080008081, 0x8000000000008009,
	0x000000000000008A, 0x0000000000000088, 0x0000000080008009, 0x000000008000000A,
	0x000000008000808B, 0x800000000000008B, 0x8000000000008089, 0x8000000000008003,
	0x8000000000008002, 0x8000000000000080, 0x000000000000800A, 0x800000008000000A,
	0x8000000080008081, 0x8000000000008080, 0x0000000080000001, 0x8000000080008008,
}

func init() {
	x, y := 1, 0
	for t := range rounds {
		rotationOffsets[5*y+x] = uint64((t + 1) * (t + 2) / 2 % 64)
		x, y = y, (2*x+3*y)%5
	}
}

// Permute runs rounds first..23 of Keccak-f[1600] on st. first = 0 runs all 24
// (plain Keccak-f); first = 1 skips round 0, which is what DeepSeek's
// DeepSeekHashV1 does.
func Permute(st *[lanes]uint64, first int) {
	if first < 0 {
		first = 0
	}
	if first > rounds {
		return
	}
	var c, d, e [5]uint64
	for round := range rounds {
		if round < first {
			continue
		}
		// θ
		for i := range 5 {
			c[i] = st[i] ^ st[i+5] ^ st[i+10] ^ st[i+15] ^ st[i+20]
		}
		for i := range 5 {
			d[i] = c[(i+4)%5] ^ rotl(c[(i+1)%5], 1)
			for j := 0; j < lanes; j += 5 {
				st[j+i] ^= d[i]
			}
		}
		// ρ and π: walk the 24-lane cycle from lane 1. The value at each source
		// lane moves to its π destination rotated by the source's offset.
		src := 1
		current := st[1]
		x, y := 1, 0
		for i := 0; i < rounds; i++ {
			x, y = y, (2*x+3*y)%5
			dst := 5*y + x
			next := st[dst]
			st[dst] = rotl(current, rotationOffsets[src])
			current, src = next, dst
		}
		// χ
		for j := 0; j < lanes; j += 5 {
			copy(e[:], st[j:j+5])
			for i := range 5 {
				st[j+i] = e[i] ^ (^e[(i+1)%5] & e[(i+2)%5])
			}
		}
		// ι
		st[0] ^= roundConstants[round]
	}
}

// Sum256 is SHA3-256 over data with every permutation running first..23.
// first = 0 is plain SHA3-256; first = 1 is DeepSeekHashV1.
func Sum256(data []byte, first int) [32]byte {
	var st [lanes]uint64
	for len(data) >= rate {
		absorbBlock(&st, data[:rate], first)
		data = data[rate:]
	}
	var tail [rate]byte
	copy(tail[:], data)
	// SHA3 padding: 0x06 after the message, 0x80 on the last byte of the block.
	tail[len(data)] ^= 0x06
	tail[rate-1] ^= 0x80
	absorbBlock(&st, tail[:], first)
	var out [32]byte
	for i := range 4 {
		for j := range 8 {
			out[8*i+j] = byte(st[i] >> (8 * j))
		}
	}
	return out
}

// absorbBlock XORs one rate-sized block into the state and permutes.
func absorbBlock(st *[lanes]uint64, block []byte, first int) {
	for i := 0; i < len(block); i += 8 {
		var lane uint64
		for j := range 8 {
			lane |= uint64(block[i+j]) << (8 * j)
		}
		st[i/8] ^= lane
	}
	Permute(st, first)
}

func rotl(v uint64, n uint64) uint64 {
	n %= 64
	if n == 0 {
		return v
	}
	return v<<n | v>>(64-n)
}
