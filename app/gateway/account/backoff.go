// Package account chooses which connection serves a request and records how the
// upstream answered: error rules, backoff, selection and per-model locks.
package account

import "time"

const (
	backoffBase   = 2 * time.Second
	backoffMax    = 5 * time.Minute
	maxLevel      = 15
	transient     = 30 * time.Second
	longCooldown  = 2 * time.Minute
	shortCooldown = 5 * time.Second
	maxReported   = 30 * time.Minute
)

// Cooldown is the lock length for a backoff level: 2 s doubling per level, at
// most 5 minutes. Levels below one count as one, and the level is bounded
// before the shift so a stored value far above the cap cannot overflow it.
func Cooldown(level int) time.Duration {
	level = min(max(level, 1), maxLevel)
	return min(backoffBase<<(level-1), backoffMax)
}

// NextLevel is the stored backoff level after one more failure.
func NextLevel(level int) int { return min(max(level, 0)+1, maxLevel) }
