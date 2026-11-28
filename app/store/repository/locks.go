package repository

import (
	"context"
	"time"

	contractsdb "github.com/goravel/framework/contracts/database/db"

	"rllmx/app/platform/text"
	"rllmx/app/store/dialect"
	"rllmx/app/store/document"
	"strings"
)

// LockPrefix starts the name of every per-model lock field in a connection's data.
const LockPrefix = modelLockPrefix

// LockKey is the data field that holds the lock of a model, or the
// account-level lock for an empty model.
func LockKey(model string) string {
	if model == "" {
		return accountLockKey
	}
	return modelLockPrefix + model
}

// LockFromLevel records a failure whose lock end comes from decide, which gets
// the backoff level read inside the transaction. Declining writes nothing; the
// result says whether a lock was written.
func (r *ConnectionsRepo) LockFromLevel(ctx context.Context, id, model string, status int, reason string, decide func(level int) (until time.Time, next int, lock bool)) (bool, error) {
	locked := false
	err := r.s.Tx(ctx, func(tx contractsdb.Tx) error {
		c, d, err := r.lockRow(tx, id)
		if err != nil {
			return err
		}
		level, _ := d.GetInt("backoffLevel")
		until, next, ok := decide(int(level))
		if !ok {
			return nil
		}
		reason = text.CutRunes(reason, maxReasonRunes)
		steps := []func() error{
			func() error { return dropEndedLocks(&d, time.Now()) },
			func() error { return d.SetString(LockKey(model), dialect.FormatTimestamp(until)) },
			func() error { return d.SetString("testStatus", "unavailable") },
			func() error { return d.SetString("lastError", reason) },
			func() error { return d.SetString("lastErrorAt", dialect.Now()) },
			func() error { return d.SetInt("errorCode", int64(status)) },
			func() error { return d.SetInt("backoffLevel", int64(next)) },
		}
		for _, step := range steps {
			if err := step(); err != nil {
				return err
			}
		}
		if _, err = r.writeData(tx, c, d); err != nil {
			return err
		}
		locked = true
		return nil
	})
	return locked, err
}

// resetHealth clears what a failure left on a connection: its error fields and
// its backoff. The locks are cleared separately, because a caller may keep them.
func resetHealth(d *document.Doc) error {
	for _, k := range []string{"lastError", "lastErrorAt", "errorCode", "rateLimitedUntil"} {
		if err := d.SetNull(k); err != nil {
			return err
		}
	}
	return d.SetInt("backoffLevel", 0)
}

// clearAllLocks nulls every per-model and account lock, so a passed check starts
// the connection clean as the reference's activation reset does.
func clearAllLocks(d *document.Doc) error {
	for _, k := range d.Keys() {
		if strings.HasPrefix(k, modelLockPrefix) {
			if err := d.SetNull(k); err != nil {
				return err
			}
		}
	}
	return nil
}

// dropEndedLocks removes the locks that ended before at, and cleared ones, so
// a connection that sees many model names does not grow a key for each of them.
func dropEndedLocks(d *document.Doc, at time.Time) error {
	for _, k := range d.Keys() {
		if !strings.HasPrefix(k, modelLockPrefix) {
			continue
		}
		if until, ok := lockExpiry(*d, k); d.IsNull(k) || (ok && !until.After(at)) {
			if err := d.Delete(k); err != nil {
				return err
			}
		}
	}
	return nil
}

// ClearLocks records a success: drops the model's lock, the account lock and
// expired locks (only expired ones for an empty model). The account turns
// healthy only when no lock is still in the future; a failure recorded after
// since is left alone so an older success cannot undo it.
func (r *ConnectionsRepo) ClearLocks(ctx context.Context, id, model string, at, since time.Time) error {
	return r.s.Tx(ctx, func(tx contractsdb.Tx) error {
		c, d, err := r.lockRow(tx, id)
		if err != nil {
			return err
		}
		if failedSince(d, since) {
			return nil
		}
		status, _ := d.GetString("testStatus")
		var drop []string
		active := false
		for _, k := range d.Keys() {
			if !strings.HasPrefix(k, modelLockPrefix) || d.IsNull(k) {
				continue
			}
			until, ok := lockExpiry(d, k)
			future := ok && until.After(at)
			switch {
			case model != "" && (k == LockKey(model) || k == accountLockKey):
				drop = append(drop, k)
			case !future:
				drop = append(drop, k)
			default:
				active = true
			}
		}
		if len(drop) == 0 && status != "unavailable" {
			return nil
		}
		for _, k := range drop {
			if err := d.SetNull(k); err != nil {
				return err
			}
		}
		if !active {
			if err := resetHealth(&d); err != nil {
				return err
			}
			if err := d.SetString("testStatus", "active"); err != nil {
				return err
			}
		}
		_, err = r.writeData(tx, c, d)
		return err
	})
}

// failedSince reports whether the connection recorded a failure after since.
// A success that began earlier must not undo it. A zero since never matches.
func failedSince(d document.Doc, since time.Time) bool {
	if since.IsZero() {
		return false
	}
	v, ok := d.GetString("lastErrorAt")
	if !ok {
		return false
	}
	t, err := dialect.ParseTimestamp(v)
	return err == nil && t.After(since)
}

func lockExpiry(d document.Doc, key string) (time.Time, bool) {
	v, ok := d.GetString(key)
	if !ok {
		return time.Time{}, false
	}
	t, err := dialect.ParseTimestamp(v)
	return t, err == nil
}
