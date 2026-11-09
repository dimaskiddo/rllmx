package oauth

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"maps"
	"time"

	"github.com/goravel/framework/contracts/log"
	"github.com/tidwall/gjson"

	"rllmx/app/cache"
	"rllmx/app/dialect"
	"rllmx/app/repository"
)

// Errors of a renewal. None carries provider text.
var (
	// ErrExpired means the refresh token is dead: the account has to be
	// connected again.
	ErrExpired = errors.New("oauth: the account needs to be reconnected")
	// ErrNoRefresh means the account cannot be renewed: no refresh token, or no
	// login for its provider.
	ErrNoRefresh = errors.New("oauth: the account has no refresh token to renew with")
	// ErrNoToken means the account holds no access token.
	ErrNoToken = errors.New("oauth: the account has no access token")
)

// ReconnectMessage is what an expired account shows. It is fixed text.
const ReconnectMessage = "needs to be reconnected"

const (
	resultTTL     = 10 * time.Second
	renewTries    = 3
	outcomeOK     = "ok"
	outcomeDead   = "dead"
	backgroundMin = 30 * time.Minute
)

// Store is the part of the connection repository a renewal uses.
type Store interface {
	Get(ctx context.Context, id string) (repository.Connection, bool, error)
	List(ctx context.Context, f repository.ConnFilter) ([]repository.Connection, error)
	SwapCredentials(ctx context.Context, id, old string, fields map[string]string) (repository.Connection, bool, error)
	MarkExpired(ctx context.Context, id, old, message string, at time.Time) (bool, error)
}

var _ Store = (*repository.ConnectionsRepo)(nil)

// Refresher renews access tokens: before a request that needs a fresh one, after
// a provider refused one, in the background and when an account is tested. One
// call per provider and refresh token is ever in flight, and a rotated token is
// written only while the stored one is still the one that was spent.
type Refresher struct {
	// Flow returns a provider's login, or false when it has none or is switched off.
	Flow   func(provider string) (Flow, bool)
	Tokens *TokenClient
	// Cache holds the 10 s result and collapses concurrent calls; nil renews
	// without either.
	Cache *cache.Cache
	// Store returns the connection store, or nil before it is open.
	Store func() Store
	Log   log.Log
	Now   func() time.Time
	// Wait pauses between tries; nil uses a real timer.
	Wait func(ctx context.Context, d time.Duration) error
}

func (r *Refresher) now() time.Time {
	if r.Now != nil {
		return r.Now()
	}
	return time.Now()
}

func (r *Refresher) wait(ctx context.Context, d time.Duration) error {
	if r.Wait != nil {
		return r.Wait(ctx, d)
	}
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-t.C:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

func (r *Refresher) store() (Store, error) {
	if r.Store == nil {
		return nil, ErrStorage
	}
	s := r.Store()
	if s == nil {
		return nil, ErrStorage
	}
	return s, nil
}

func (r *Refresher) warn(format string, args ...any) {
	if r.Log != nil {
		r.Log.Warningf(format, args...)
	}
}

// Ready renews a connection's token first when it is due. A connection that
// is not an OAuth login, or is not due, comes back unchanged. On any error the
// connection as it was comes back with it, so a caller can go on with the old
// token unless the error is ErrExpired.
func (r *Refresher) Ready(ctx context.Context, conn repository.Connection) (repository.Connection, error) {
	if conn.AuthType != "oauth" || conn.Corrupt {
		return conn, nil
	}
	f, ok := r.Flow(conn.Provider)
	if !ok || !Due(f, conn.Doc(), r.now()) {
		return conn, nil
	}
	fresh, err := r.Refresh(ctx, conn.ID, "")
	if err != nil {
		return conn, err
	}
	return fresh, nil
}

// Renew is Ready for a token the provider just refused: up to three tries, one
// and two seconds apart. If the stored token is no longer the refused one,
// someone else already renewed it and the stored connection comes back at once.
func (r *Refresher) Renew(ctx context.Context, conn repository.Connection) (repository.Connection, error) {
	if conn.AuthType != "oauth" || conn.Corrupt {
		return conn, ErrNoRefresh
	}
	rejected, _ := conn.Doc().GetString("accessToken")
	var last error
	for try := range renewTries {
		if try > 0 {
			if err := r.wait(ctx, time.Duration(try)*time.Second); err != nil {
				return conn, err
			}
		}
		fresh, err := r.Refresh(ctx, conn.ID, rejected)
		if err == nil {
			return fresh, nil
		}
		last = err
		if errors.Is(err, ErrExpired) || errors.Is(err, ErrNoRefresh) || errors.Is(err, ErrStorage) || ctx.Err() != nil {
			break
		}
	}
	return conn, last
}

// Refresh renews one connection. rejected is the access token a provider just
// refused, or "" when the caller only found it due; when the stored token is
// something else, it was renewed already and nothing is called.
func (r *Refresher) Refresh(ctx context.Context, id, rejected string) (repository.Connection, error) {
	st, err := r.store()
	if err != nil {
		return repository.Connection{}, err
	}
	conn, ok, err := st.Get(ctx, id)
	if err != nil || !ok {
		return conn, ErrStorage
	}
	if conn.Corrupt {
		return conn, ErrNoRefresh
	}
	if status, _ := conn.Doc().GetString("testStatus"); status == repository.StatusExpired {
		return conn, ErrExpired
	}
	f, ok := r.Flow(conn.Provider)
	if !ok {
		return conn, ErrNoRefresh
	}
	d := conn.Doc()
	rt, _ := d.GetString("refreshToken")
	if rt == "" {
		return conn, ErrNoRefresh
	}
	if access, _ := d.GetString("accessToken"); rejected != "" && access != rejected {
		return conn, nil
	}

	sum := sha256.Sum256([]byte(rt))
	key := conn.Provider + ":" + hex.EncodeToString(sum[:])
	load := func(ctx context.Context) ([]byte, error) { return r.exchange(ctx, st, f, conn, rt) }
	var raw []byte
	if r.Cache != nil {
		raw, err = r.Cache.Load(ctx, cache.Refresh, key, resultTTL, load)
	} else {
		raw, err = load(ctx)
	}
	if err != nil {
		return conn, err
	}
	fresh, ok, gerr := st.Get(ctx, id)
	if gerr != nil || !ok {
		return conn, ErrStorage
	}
	if string(raw) == outcomeDead {
		return fresh, ErrExpired
	}
	if status, _ := fresh.Doc().GetString("testStatus"); status == repository.StatusExpired {
		return fresh, ErrExpired
	}
	return fresh, nil
}

// exchange spends the refresh token once and stores the answer. It runs inside
// the cache's single flight.
func (r *Refresher) exchange(ctx context.Context, st Store, f Flow, conn repository.Connection, rt string) ([]byte, error) {
	psd, _ := conn.Doc().GetRaw("providerSpecificData")
	headers := callHeaders(f, gjson.Get(psd, deviceIDKey).String())
	t, err := r.Tokens.Refresh(ctx, f, rt, headers)
	if err != nil {
		if ClassOf(err) != ClassInvalidGrant {
			return nil, err
		}
		if _, merr := st.MarkExpired(ctx, conn.ID, rt, ReconnectMessage, r.now()); merr != nil {
			return nil, ErrStorage
		}
		return []byte(outcomeDead), nil
	}
	now := r.now()
	fields := map[string]string{
		"accessToken":   quote(t.Access),
		"refreshToken":  quote(t.Refresh),
		"lastRefreshAt": quote(dialect.FormatTimestamp(now)),
		"expiresAt":     "null",
	}
	if t.ID != "" {
		fields["idToken"] = quote(t.ID)
	}
	var expires time.Time
	if t.ExpiresIn > 0 {
		fields["expiresIn"] = jsonInt(t.ExpiresIn)
		expires = now.Add(time.Duration(t.ExpiresIn) * time.Second)
	}
	if h := refreshHooks[f.Refresh.Hook]; h != nil {
		// A failed step keeps the key the account already has.
		if upd, herr := h(ctx, r.Tokens, f, t, headers); herr == nil {
			maps.Copy(fields, upd.Fields)
			if !upd.ExpiresAt.IsZero() && (expires.IsZero() || upd.ExpiresAt.Before(expires)) {
				expires = upd.ExpiresAt
			}
		} else {
			r.warn("oauth: a %s renewal step failed: %s", conn.Provider, string(ClassOf(herr)))
		}
	}
	if !expires.IsZero() {
		fields["expiresAt"] = quote(dialect.FormatTimestamp(expires))
	}
	if _, _, err := st.SwapCredentials(ctx, conn.ID, rt, fields); err != nil {
		return nil, ErrStorage
	}
	return []byte(outcomeOK), nil
}

// Ensure is what testing an account does: renew the token when it is due, then
// check one is held and, when it cannot be renewed, that it has not run out.
// refreshed says whether a call was made.
func (r *Refresher) Ensure(ctx context.Context, id string) (refreshed bool, err error) {
	st, err := r.store()
	if err != nil {
		return false, err
	}
	conn, ok, err := st.Get(ctx, id)
	if err != nil || !ok {
		return false, ErrStorage
	}
	if conn.Corrupt {
		return false, ErrNoRefresh
	}
	d := conn.Doc()
	if status, _ := d.GetString("testStatus"); status == repository.StatusExpired {
		return false, ErrExpired
	}
	if access, _ := d.GetString("accessToken"); access == "" {
		return false, ErrNoToken
	}
	f, ok := r.Flow(conn.Provider)
	if !ok {
		return false, ErrNoRefresh
	}
	rt, _ := d.GetString("refreshToken")
	if rt == "" {
		if exp, known := timeOf(d, "expiresAt"); known && !exp.After(r.now()) {
			return false, ErrNoRefresh
		}
		return false, nil
	}
	if !Due(f, d, r.now()) {
		return false, nil
	}
	if _, err := r.Refresh(ctx, id, ""); err != nil {
		return false, err
	}
	return true, nil
}
