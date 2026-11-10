package stream

import "rllmx/app/llm/translate"

// Tracker accumulates what an upstream reported about its own usage. It is fed
// the upstream's payloads before any translation, so the stored row comes from
// one reading of what the provider said.
type Tracker struct {
	format   string
	usage    translate.Usage
	reported bool
	content  int
}

// NewTracker returns a tracker for an upstream speaking format.
func NewTracker(format string) *Tracker { return &Tracker{format: format} }

// Feed reads one upstream event. Partial reports merge field by field by
// maximum, because Claude splits its counts over two events.
func (t *Tracker) Feed(data []byte) {
	if u, ok := translate.ReadUsageEvent(t.format, data); ok {
		t.usage = translate.MergeUsage(t.usage, u)
		t.reported = t.reported || u.Prompt > 0 || u.Completion > 0
	}
}

// AddText counts streamed characters, the basis of an estimate when the
// upstream reports nothing.
func (t *Tracker) AddText(n int) { t.content += n }

// ContentLen is the number of characters streamed so far.
func (t *Tracker) ContentLen() int { return t.content }

// Final returns the usage to store. With no report from the upstream it is
// estimated from the request size and the output length, at about four
// characters a token, and marked as estimated. With no output either it stays
// empty.
func (t *Tracker) Final(requestBytes int) translate.Usage {
	if t.reported {
		return translate.NormalizeUsage(t.usage)
	}
	return Estimate(requestBytes, t.content)
}

// Estimate is the usage of an answer the upstream did not count: a quarter of
// the bytes sent and received, flagged as an estimate. An answer with no text
// has none.
func Estimate(requestBytes, contentLen int) translate.Usage {
	if contentLen == 0 {
		return translate.Usage{}
	}
	return translate.Usage{
		Prompt:     int64((requestBytes + 3) / 4),
		Completion: int64(max(contentLen/4, 1)),
		Estimated:  true,
	}
}
