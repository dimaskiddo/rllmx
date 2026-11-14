package qoder

import "testing"

// FuzzQueueSignal reads the queue signal out of any answer. It must never
// panic, and a signal it reports names no more than the bounded fields it reads.
func FuzzQueueSignal(f *testing.F) {
	f.Add([]byte(`{"code":"429","isQueued":true,"modelKey":"m","queueType":"q"}`))
	f.Add([]byte(`{"body":"{\"isQueued\":true}"}`))
	f.Add([]byte(`[[[[[[[[[[[[[[[[[[[[[[[[[[[[[[[[[[[[`))
	f.Fuzz(func(t *testing.T, raw []byte) {
		if q := qoderQueueInfo(raw); q != nil && q.retryAfter < 0 {
			t.Fatalf("a negative wait: %+v", q)
		}
	})
}

// FuzzFrameStatus reads a refusal out of the first frame of any stream. It
// must never panic, and a status it returns is a real HTTP status.
func FuzzFrameStatus(f *testing.F) {
	f.Add([]byte("data: {\"code\":429}\n\n"))
	f.Add([]byte("data: {\"statusCode\":401}\n"))
	f.Add([]byte("no frame here"))
	f.Fuzz(func(t *testing.T, seen []byte) {
		if status, ok := qoderFrameStatus(seen); ok && (status < 100 || status > 599) {
			t.Fatalf("status %d is not an HTTP status", status)
		}
	})
}
