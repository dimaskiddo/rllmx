package skill

// ECCOptions is the imported catalog router's switch and bounds.
type ECCOptions struct {
	Enabled    bool
	Confidence float64
	MaxSkills  int
}

// ClassifyECC scores the query against the imported catalog. A zero confidence
// is the default threshold; a count outside one to five is clamped, so a bad
// settings value cannot inject the whole catalog.
func ClassifyECC(reg *Registry, query string, o ECCOptions) []Match {
	if reg == nil || !o.Enabled {
		return nil
	}
	floor := o.Confidence
	if floor <= 0 || floor > 1 {
		floor = DefaultThreshold
	}
	count := o.MaxSkills
	if count <= 0 {
		count = 1
	}
	count = min(count, 5)
	out := reg.eccIndex().Score(query, floor)
	if len(out) > count {
		out = out[:count]
	}
	return out
}
