package tests

import "time"

// Account is an executor request's account lookup backed by a map.
func Account(kv map[string]string) func(string) string {
	return func(k string) string { return kv[k] }
}

// InstantSleep makes every retry wait cost nothing and records the waits asked for.
func InstantSleep(waits *[]time.Duration) func(time.Duration) <-chan time.Time {
	return func(d time.Duration) <-chan time.Time {
		*waits = append(*waits, d)
		c := make(chan time.Time, 1)
		c <- time.Now()
		return c
	}
}
