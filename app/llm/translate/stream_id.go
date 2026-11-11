package translate

import (
	"crypto/rand"
	"encoding/hex"
)

// WeakStreamID reports an id a strict client would refuse: empty, a generic
// word or shorter than eight characters.
func WeakStreamID(id string) bool {
	return id == "" || id == "chat" || id == "completion" || len(id) < 8
}

// StrictStreamID returns in when a strict client accepts it, and a fresh random
// chatcmpl id otherwise.
func StrictStreamID(in string) string {
	if !WeakStreamID(in) {
		return in
	}
	var b [8]byte
	_, _ = rand.Read(b[:])
	return "chatcmpl-" + hex.EncodeToString(b[:])
}
