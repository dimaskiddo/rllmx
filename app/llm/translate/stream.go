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

// Gate holds the finish of a turn until its usage arrives.
type Gate struct {
	// Finish is the turn's finish reason once a chunk carried one.
	Finish string
	// HasFinish reports that Finish was seen.
	HasFinish bool
	// Released reports that the finish was already handed over.
	Released bool
	// Usage is what the turn reported, merged field by field.
	Usage Usage
	// HasUsage reports that a usage report arrived.
	HasUsage bool
}

// See records what a chunk carries and reports whether the finish may be
// released now: it is known, and so is the usage or the chunk is the usage.
func (g *Gate) See(c Chunk) bool {
	if c.Usage != nil {
		g.Usage = MergeUsage(g.Usage, *c.Usage)
		g.HasUsage = true
	}
	if c.FinishReason != "" && !g.HasFinish {
		g.Finish, g.HasFinish = c.FinishReason, true
	}
	if g.Released || !g.HasFinish || !g.HasUsage {
		return false
	}
	g.Released = true
	return true
}

func (g *Gate) Held() bool { return g.HasFinish && !g.Released }

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
