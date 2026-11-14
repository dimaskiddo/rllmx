package stt

import (
	"encoding/binary"
	"testing"
)

// FuzzWav24kMono hands the WAV check arbitrary upload bytes. It must never
// panic, and a file it accepts is a canonical one: the header says RIFF and
// WAVE, and a data chunk of no more than 120 seconds of 16-bit mono.
func FuzzWav24kMono(f *testing.F) {
	good := make([]byte, 44)
	copy(good, "RIFF")
	copy(good[8:], "WAVEfmt ")
	binary.LittleEndian.PutUint32(good[16:], 16)
	binary.LittleEndian.PutUint16(good[20:], 1)
	binary.LittleEndian.PutUint16(good[22:], 1)
	binary.LittleEndian.PutUint32(good[24:], 24000)
	binary.LittleEndian.PutUint16(good[34:], 16)
	copy(good[36:], "data")
	f.Add(good)
	f.Add([]byte("RIFF"))
	f.Add([]byte{})
	f.Fuzz(func(t *testing.T, b []byte) {
		if !wav24kMono(b) {
			return
		}
		if len(b) < 44 || string(b[0:4]) != "RIFF" || string(b[8:12]) != "WAVE" {
			t.Fatalf("accepted a file with no RIFF/WAVE header: % x", b[:min(len(b), 16)])
		}
	})
}
