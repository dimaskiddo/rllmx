package skill

// ClassifyLocal scores the query against the routable installed skills and
// returns at most two, strongest first, each one clearing its own threshold.
// cands are the skills already resolved as enabled and smart, so a disabled or
// always-on skill never reaches the scorer.
func ClassifyLocal(reg *Registry, cands []Manifest, query string) []Match {
	if reg == nil || len(cands) == 0 {
		return nil
	}
	floor := cands[0].Threshold()
	byID := make(map[string]float64, len(cands))
	for _, m := range cands {
		byID[m.ID] = m.Threshold()
		floor = min(floor, m.Threshold())
	}
	var out []Match
	for _, m := range reg.localIndex().Score(query, floor) {
		th, ok := byID[m.ID]
		if !ok || m.Score < th {
			continue
		}
		out = append(out, m)
		if len(out) == maxLocal {
			break
		}
	}
	return out
}
