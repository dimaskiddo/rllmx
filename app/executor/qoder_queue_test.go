package executor

import (
	"testing"
	"time"
)

func TestAQueuedAnswerIsReadThroughItsWrappers(t *testing.T) {
	inner := `{"isQueued":true,"modelKey":"auto","queueCount":0,"queueType":"p3","retryAfterSeconds":30,"serviceAvailable":false,"subCode":"RESOURCE_LIMIT"}`
	for name, raw := range map[string]string{
		"the message twice encoded": `{"code":"10605","message":` + quoted(inner) + `,"subCode":"RESOURCE_LIMIT"}`,
		"inside an envelope":        `{"statusCodeValue":200,"body":` + quoted(`{"error":{"data":`+quoted(inner)+`}}`) + `}`,
		"a bare inner object":       inner,
		"the code alone":            `{"code":"10605"}`,
		"unavailable alone":         `{"serviceAvailable":false}`,
	} {
		info := qoderQueueInfo([]byte(raw))
		if info == nil {
			t.Errorf("%s: no queue signal in %s", name, raw)
			continue
		}
		if !info.queued {
			t.Errorf("%s: not queued", name)
		}
	}
	info := qoderQueueInfo([]byte(`{"code":"10605","message":` + quoted(inner) + `}`))
	if info.modelKey != "auto" || info.queueType != "p3" || info.retryAfter != 30*time.Second {
		t.Errorf("%+v", info)
	}
}

func TestAnAnswerThatSaysNothingAboutAQueueIsNotOne(t *testing.T) {
	for name, raw := range map[string]string{
		"empty":         "",
		"a plain chunk": `{"choices":[{"delta":{"content":"hi"}}]}`,
		"another code":  `{"code":"110","message":"billing"}`,
		"queued false":  `{"isQueued":false,"serviceAvailable":true}`,
		"not json":      `not json at all`,
	} {
		if info := qoderQueueInfo([]byte(raw)); info != nil {
			t.Errorf("%s: read as queued: %+v", name, info)
		}
	}
}

func TestTheQueueWaitIsClamped(t *testing.T) {
	for name, tc := range map[string]struct {
		info       *qoderQueue
		fromStatus bool
		want       time.Duration
	}{
		"no hint":            {nil, false, 30 * time.Second},
		"a short hint":       {&qoderQueue{retryAfter: 2 * time.Second}, false, 15 * time.Second},
		"a long hint":        {&qoderQueue{retryAfter: 10 * time.Minute}, false, 60 * time.Second},
		"past the ceiling":   {&qoderQueue{retryAfter: 10 * time.Hour}, false, 60 * time.Second},
		"inside the range":   {&qoderQueue{retryAfter: 20 * time.Second}, false, 20 * time.Second},
		"a status hint":      {&qoderQueue{retryAfter: 2 * time.Second}, true, 30 * time.Second},
		"a status long hint": {&qoderQueue{retryAfter: 45 * time.Second}, true, 45 * time.Second},
	} {
		if got := qoderQueueDelay(tc.info, tc.fromStatus); got != tc.want {
			t.Errorf("%s: %s, want %s", name, got, tc.want)
		}
	}
}

func TestADeeplyNestedBodyIsBounded(t *testing.T) {
	// The walk stops at its depth, so a body nested past it is not read. The
	// nesting is objects rather than JSON strings: each string level would
	// escape the one inside it, and the body would grow exponentially.
	const depth = 12
	raw := `{"isQueued":true}`
	for range depth {
		raw = `{"error":` + raw + `}`
	}
	if info := qoderQueueInfo([]byte(raw)); info != nil {
		t.Errorf("%d levels deep was read: %+v", depth+1, info)
	}
	// A body inside the bound is still read.
	shallow := `{"isQueued":true}`
	for range 3 {
		shallow = `{"message":` + shallow + `}`
	}
	if info := qoderQueueInfo([]byte(shallow)); info == nil || !info.queued {
		t.Errorf("a body inside the bound was not read: %+v", info)
	}
}

// quoted writes s as a JSON string literal.
func quoted(s string) string {
	out := make([]byte, 0, len(s)+2)
	out = append(out, '"')
	for i := 0; i < len(s); i++ {
		switch s[i] {
		case '"', '\\':
			out = append(out, '\\', s[i])
		default:
			out = append(out, s[i])
		}
	}
	return string(append(out, '"'))
}
