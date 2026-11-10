package repository

import (
	"context"
	"time"

	"github.com/tidwall/gjson"

	contractsdb "github.com/goravel/framework/contracts/database/db"

	"fmt"
	"rllmx/app/dialect"
	"rllmx/app/document"
	"slices"
)

// TouchUsageAt bumps the use counter and stamps lastUsedAt with a caller-given
// time, so callers that order uses can keep the timestamps strictly increasing.
func (r *ConnectionsRepo) TouchUsageAt(ctx context.Context, id string, sticky bool, at time.Time) error {
	return r.s.Tx(ctx, func(tx contractsdb.Tx) error {
		c, d, err := r.lockRow(tx, id)
		if err != nil {
			return err
		}
		count := int64(1)
		if sticky {
			prev, _ := d.GetInt("consecutiveUseCount")
			count = prev + 1
		}
		if err := d.SetInt("consecutiveUseCount", count); err != nil {
			return err
		}
		if err := d.SetString("lastUsedAt", dialect.FormatTimestamp(at)); err != nil {
			return err
		}
		_, err = r.writeData(tx, c, d)
		return err
	})
}

// SwapCredentials merges fields into a connection only while its stored
// refresh token is still old. A refresh token that changed means another
// request or process already rotated it, so nothing is written and the current
// row comes back with swapped false.
func (r *ConnectionsRepo) SwapCredentials(ctx context.Context, id, old string, fields map[string]string) (out Connection, swapped bool, err error) {
	err = r.s.Tx(ctx, func(tx contractsdb.Tx) error {
		c, d, err := r.lockRow(tx, id)
		if err != nil {
			return err
		}
		out = c
		if cur, _ := d.GetString("refreshToken"); cur != old {
			return nil
		}
		if err := applyFields(&d, fields); err != nil {
			return err
		}
		if out, err = r.writeData(tx, c, d); err != nil {
			return err
		}
		swapped = true
		return nil
	})
	return out, swapped, err
}

// PatchPSD sets and removes keys of a connection's providerSpecificData in one
// transaction, so concurrent patches of different keys all survive. A key may
// not appear in both set and remove.
func (r *ConnectionsRepo) PatchPSD(ctx context.Context, id string, set map[string]string, remove []string) error {
	return r.s.Tx(ctx, func(tx contractsdb.Tx) error {
		c, d, err := r.lockRow(tx, id)
		if err != nil {
			return err
		}
		psd, err := patchObject(d, "providerSpecificData", set, remove)
		if err != nil {
			return err
		}
		if err := d.SetRaw("providerSpecificData", psd); err != nil {
			return err
		}
		_, err = r.writeData(tx, c, d)
		return err
	})
}

// patchObject applies set and remove to the object stored under key in d and
// returns the new object text. A missing or non-object value starts as {}.
func patchObject(d document.Doc, key string, set map[string]string, remove []string) (string, error) {
	for k, v := range set {
		if k == "" || !gjson.Valid(v) {
			return "", fmt.Errorf("repository: %q is not a key with a valid JSON value", k)
		}
		if slices.Contains(remove, k) {
			return "", fmt.Errorf("repository: %q is both set and removed", k)
		}
	}
	raw, _ := d.GetRaw(key)
	if !gjson.Parse(raw).IsObject() {
		raw = "{}"
	}
	obj, err := document.ParseDoc(raw)
	if err != nil {
		return "", err
	}
	for _, k := range sortedKeys(set) {
		if err := obj.SetRaw(k, set[k]); err != nil {
			return "", err
		}
	}
	for _, k := range remove {
		if err := obj.Delete(k); err != nil {
			return "", err
		}
	}
	return obj.String(), nil
}

// RecordTest stores the outcome of a key check on a connection. A pass sets it
// active and clears the last error; a failure sets it to error with the given
// message, which is a fixed one and never provider text. A check is not a
// request, so it touches no lock, no backoff level and no error code: it does
// not create a cooldown and a pass does not erase a real one. A connection
// marked expired stays expired, so the selector keeps skipping it.
func (r *ConnectionsRepo) RecordTest(ctx context.Context, id string, ok bool, message string, at time.Time) error {
	return r.s.Tx(ctx, func(tx contractsdb.Tx) error {
		c, d, err := r.lockRow(tx, id)
		if err != nil {
			return err
		}
		stamp := dialect.FormatTimestamp(at)
		if err := d.SetString("lastTested", stamp); err != nil {
			return err
		}
		status, _ := d.GetString("testStatus")
		switch {
		case ok:
			if status != StatusExpired {
				if err := d.SetString("testStatus", "active"); err != nil {
					return err
				}
			}
			for _, k := range []string{"lastError", "lastErrorAt"} {
				if err := d.SetNull(k); err != nil {
					return err
				}
			}
		default:
			if status != StatusExpired {
				if err := d.SetString("testStatus", "error"); err != nil {
					return err
				}
			}
			if runes := []rune(message); len(runes) > maxReasonRunes {
				message = string(runes[:maxReasonRunes])
			}
			if err := d.SetString("lastError", message); err != nil {
				return err
			}
			if err := d.SetString("lastErrorAt", stamp); err != nil {
				return err
			}
		}
		_, err = r.writeData(tx, c, d)
		return err
	})
}

// MarkExpired records that a connection's refresh token was refused for good:
// testStatus becomes expired, so the selector skips it, with a fixed message
// and the time. It writes only while the stored refresh token is still old, as
// SwapCredentials does, so a token another caller already rotated is not
// declared dead on a stale answer. It reports whether it wrote.
func (r *ConnectionsRepo) MarkExpired(ctx context.Context, id, old, message string, at time.Time) (bool, error) {
	var marked bool
	err := r.s.Tx(ctx, func(tx contractsdb.Tx) error {
		c, d, err := r.lockRow(tx, id)
		if err != nil {
			return err
		}
		if cur, _ := d.GetString("refreshToken"); cur != old {
			return nil
		}
		for k, v := range map[string]string{"testStatus": StatusExpired, "lastError": message, "lastErrorAt": dialect.FormatTimestamp(at)} {
			if err := d.SetString(k, v); err != nil {
				return err
			}
		}
		if _, err = r.writeData(tx, c, d); err != nil {
			return err
		}
		marked = true
		return nil
	})
	return marked, err
}

// SetProjectID stores a Cloud Code project on a connection that has none. A
// project already there is kept, so a lookup that raced with a login or with
// the user's own setting never overwrites it. It reports whether it wrote.
func (r *ConnectionsRepo) SetProjectID(ctx context.Context, id, project string) (wrote bool, err error) {
	err = r.s.Tx(ctx, func(tx contractsdb.Tx) error {
		c, d, err := r.lockRow(tx, id)
		if err != nil {
			return err
		}
		if cur, _ := d.GetString("projectId"); cur != "" {
			return nil
		}
		if err := d.SetString("projectId", project); err != nil {
			return err
		}
		if _, err := r.writeData(tx, c, d); err != nil {
			return err
		}
		wrote = true
		return nil
	})
	return wrote, err
}
