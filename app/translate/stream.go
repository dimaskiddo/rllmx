package translate

// Frame is one unit a stream writer hands to the transport: an optional event
// name, the payload and, for Done, the end-of-stream marker some formats use.
type Frame struct {
	Event string
	Data  []byte
	Done  bool
}

// StreamReader turns one format's stream events into hub chunks. Read gets one
// event at a time; a payload that is not valid for the format yields nothing.
// Flush runs when the upstream ends and returns whatever it left open.
type StreamReader interface {
	Read(event string, data []byte) []Chunk
	Flush() []Chunk
}

// StreamWriter turns hub chunks into one format's frames. It holds the closing
// event of a turn until a usage report arrives or Close runs, so the client
// gets the counts whichever order the upstream sent them in. An error chunk
// is not a writer's business: the transport answers it with the failure frame.
type StreamWriter interface {
	Write(Chunk) []Frame
	// Held reports a finish waiting for its usage report.
	Held() bool
	// Close ends the stream: it releases a held finish and writes the end marker.
	Close() []Frame
}

// gate holds the finish of a turn until its usage arrives.
type gate struct {
	finish   string
	hasFin   bool
	released bool
	usage    Usage
	hasUsage bool
}

// see records what a chunk carries and reports whether the finish may be
// released now: it is known, and so is the usage or the chunk is the usage.
func (g *gate) see(c Chunk) bool {
	if c.Usage != nil {
		g.usage = MergeUsage(g.usage, *c.Usage)
		g.hasUsage = true
	}
	if c.FinishReason != "" && !g.hasFin {
		g.finish, g.hasFin = c.FinishReason, true
	}
	if g.released || !g.hasFin || !g.hasUsage {
		return false
	}
	g.released = true
	return true
}

func (g *gate) held() bool { return g.hasFin && !g.released }

// MergeUsage combines two partial reports field by field by maximum, the way
// an upstream that splits its counts over several events needs.
func MergeUsage(a, b Usage) Usage {
	a.Prompt = max(a.Prompt, b.Prompt)
	a.Completion = max(a.Completion, b.Completion)
	a.Cached = max(a.Cached, b.Cached)
	a.CacheWrite = max(a.CacheWrite, b.CacheWrite)
	a.Reasoning = max(a.Reasoning, b.Reasoning)
	a.Estimated = a.Estimated && b.Estimated
	return a
}
