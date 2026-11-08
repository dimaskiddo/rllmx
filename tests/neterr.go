package tests

// TimeoutError is a network error that reports a timeout, the way a read
// deadline does, so a test can check how a caller classifies one.
type TimeoutError struct{}

func (TimeoutError) Error() string   { return "i/o timeout" }
func (TimeoutError) Timeout() bool   { return true }
func (TimeoutError) Temporary() bool { return true }
