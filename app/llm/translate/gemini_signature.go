package translate

// SignatureFor picks the thought signature a call goes out with: the one the
// client returned, else one remembered from the answer that created it.
func (c *Ctx) SignatureFor(tc ToolCall) string {
	if tc.Signature != "" {
		return tc.Signature
	}
	if c.Calls != nil {
		if s, ok := c.Calls.Get(tc.ID); ok {
			return s
		}
	}
	return ""
}
