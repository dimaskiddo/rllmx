package executor

import (
	"strconv"
	"strings"
	"time"

	"github.com/tidwall/gjson"
)

// The queue's own words. The code alone is the signal, even when the object it
// usually travels with is missing.
const (
	qoderQueueCode = "10605"
	// qoderQueueDepth bounds the walk: the payload is nested three or four deep,
	// so eight is room to spare and keeps a crafted body from costing anything.
	qoderQueueDepth = 8
	// qoderQueueRetry and the bounds around it are the queue's own interval: a
	// sub-second hint made the throttle worse.
	qoderQueueRetry       = 30 * time.Second
	qoderQueueMinRetry    = 15 * time.Second
	qoderQueueMaxRetry    = 60 * time.Second
	qoderStatusRetryFloor = 30 * time.Second
	// qoderQueueMaxWait is how long a request is held for a queued model before
	// it answers with a retryable refusal.
	qoderQueueMaxWait = 10 * time.Minute
)

// qoderQueue is what an answer said about the model's queue.
type qoderQueue struct {
	queued     bool
	modelKey   string
	queueType  string
	retryAfter time.Duration
}

// qoderQueueInfo reads the queue signal out of an answer that may hold it
// nested inside a JSON string inside another. It returns nil when nothing in
// the answer says the model is queued.
func qoderQueueInfo(raw []byte) *qoderQueue {
	if len(raw) == 0 {
		return nil
	}
	root := gjson.ParseBytes(raw)
	if !root.Exists() {
		return nil
	}
	info := &qoderQueue{}
	var code, subCode string
	var isQueued, serviceAvailable *bool
	walkJSON(root, 0, func(v gjson.Result) bool {
		if !v.IsObject() {
			return true
		}
		if code == "" {
			code = textOf(v.Get("code"))
		}
		if subCode == "" {
			subCode = textOf(v.Get("subCode"))
		}
		if isQueued == nil {
			isQueued = boolOf(v.Get("isQueued"))
		}
		if serviceAvailable == nil {
			serviceAvailable = boolOf(v.Get("serviceAvailable"))
		}
		if info.modelKey == "" {
			info.modelKey = textOf(v.Get("modelKey"))
		}
		if info.queueType == "" {
			info.queueType = textOf(v.Get("queueType"))
		}
		if info.retryAfter == 0 {
			if n, ok := numberOf(v.Get("retryAfterSeconds")); ok {
				info.retryAfter = time.Duration(n * float64(time.Second))
			} else if n, ok := numberOf(v.Get("waitTime")); ok {
				info.retryAfter = time.Duration(n * float64(time.Second))
			}
		}
		return true
	})
	info.queued = (isQueued != nil && *isQueued) ||
		(serviceAvailable != nil && !*serviceAvailable) ||
		subCode == "RESOURCE_LIMIT" || code == qoderQueueCode
	if !info.queued {
		return nil
	}
	return info
}

// walkJSON visits every object reachable from v, following the keys an envelope
// nests under and parsing a JSON string once. seen keeps a value from being
// parsed twice, and depth keeps a crafted body bounded.
func walkJSON(v gjson.Result, depth int, visit func(gjson.Result) bool) {
	if depth > qoderQueueDepth {
		return
	}
	switch {
	case v.IsArray():
		for _, item := range v.Array() {
			walkJSON(item, depth+1, visit)
		}
		return
	case !v.IsObject():
		return
	}
	if !visit(v) {
		return
	}
	for _, key := range []string{"error", "data", "body", "message", "cause", "result", "payload"} {
		child := v.Get(key)
		switch {
		case child.IsObject() || child.IsArray():
			walkJSON(child, depth+1, visit)
		case child.Type == gjson.String:
			if inner := gjson.Parse(child.String()); inner.IsObject() || inner.IsArray() {
				walkJSON(inner, depth+1, visit)
			}
		}
	}
}

// qoderQueueDelay is the wait before the next attempt, clamped to the interval
// the queue clears in. A hint from the status endpoint never drops below the
// default: that endpoint has been seen saying "not queued" while the chat call
// was still refused.
func qoderQueueDelay(info *qoderQueue, fromStatus bool) time.Duration {
	d := qoderQueueRetry
	if info != nil && info.retryAfter > 0 {
		d = info.retryAfter
	}
	if fromStatus && d < qoderStatusRetryFloor {
		d = qoderStatusRetryFloor
	}
	if d < qoderQueueMinRetry {
		d = qoderQueueMinRetry
	}
	if d > qoderQueueMaxRetry {
		d = qoderQueueMaxRetry
	}
	return d
}

// textOf reads a string or number as trimmed text.
func textOf(v gjson.Result) string {
	switch v.Type {
	case gjson.String:
		return strings.TrimSpace(v.String())
	case gjson.Number:
		return strings.TrimSpace(v.Raw)
	}
	return ""
}

// boolOf reads a boolean written as one or as its word.
func boolOf(v gjson.Result) *bool {
	switch {
	case v.Type == gjson.True:
		t := true
		return &t
	case v.Type == gjson.False:
		f := false
		return &f
	}
	switch textOf(v) {
	case "true":
		t := true
		return &t
	case "false":
		f := false
		return &f
	}
	return nil
}

// numberOf reads a count written as a number or as its text.
func numberOf(v gjson.Result) (float64, bool) {
	switch v.Type {
	case gjson.Number:
		return v.Float(), true
	case gjson.String:
		n, err := strconv.ParseFloat(strings.TrimSpace(v.String()), 64)
		return n, err == nil
	}
	return 0, false
}
