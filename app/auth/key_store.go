package auth

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"time"

	"rllmx/app/cache"
	"rllmx/app/identity"
	"rllmx/app/repository"
)

// KeyTTL is how long a key lookup is cached. A revoked key stops working within
// this window, or immediately after Keys.Changed.
const KeyTTL = 10 * time.Second

// Keys resolves a presented key to a caller. A hit is cached; a miss is not,
// because a miss is what a guessing client produces and the store should answer
// it every time.
type Keys struct {
	DB    func() *repository.Store
	Cache *cache.Cache
	Codec identity.Keys
}

// NewKeys builds the key resolver.
func NewKeys(db func() *repository.Store, c *cache.Cache, codec identity.Keys) *Keys {
	return &Keys{DB: db, Cache: c, Codec: codec}
}

// Lookup decides what a presented key is. A malformed key is refused before any
// query; anything else is looked up by exact sealed match, so a checksum
// mismatch still works when the row matches, as the reference behaves.
func (k *Keys) Lookup(ctx context.Context, key string) identity.KeyResult {
	if key == "" {
		return identity.KeyResult{Status: identity.KeyNone}
	}
	if k.Codec.Classify(key) == identity.ClassMalformed {
		return identity.KeyResult{Status: identity.KeyInvalid}
	}
	if k.Cache != nil {
		if raw, ok := k.Cache.Get(cache.APIKeys, cacheKey(key)); ok {
			if res, ok := decodeResult(raw); ok {
				return res
			}
		}
	}

	db := k.DB()
	if db == nil {
		return identity.KeyResult{Status: identity.KeyInvalid}
	}
	row, found, err := db.APIKeys().GetByKey(ctx, key)
	if err != nil || !found || !row.IsActive || row.Unreadable {
		return identity.KeyResult{Status: identity.KeyInvalid}
	}
	res := identity.KeyResult{
		Status: identity.KeyValid,
		Caller: identity.Caller{
			ID:         row.ID,
			Name:       row.Name,
			Restricted: row.AccessRestricted,
			Allow:      row.AccessAllow,
		},
	}
	if k.Cache != nil {
		k.Cache.Set(cache.APIKeys, cacheKey(key), encodeResult(res), KeyTTL)
	}
	return res
}

// Changed drops every cached key, for a key this process created, revoked or
// edited.
func (k *Keys) Changed() {
	if k.Cache != nil {
		k.Cache.Purge(cache.APIKeys)
	}
}

// cacheKey is the hash of the key, so a raw secret never lands in a shared
// store or a cache dump.
func cacheKey(key string) string {
	sum := sha256.Sum256([]byte(key))
	return hex.EncodeToString(sum[:])
}

// keyEntry is the cached form of a lookup decision. A key id never changes, so
// the snapshot is what the guard needs.
type keyEntry struct {
	Valid      bool     `json:"valid"`
	ID         string   `json:"id"`
	Name       string   `json:"name"`
	Restricted bool     `json:"restricted"`
	Allow      []string `json:"allow"`
}

func encodeResult(r identity.KeyResult) []byte {
	e := keyEntry{Valid: r.Status == identity.KeyValid, ID: r.Caller.ID, Name: r.Caller.Name, Restricted: r.Caller.Restricted, Allow: r.Caller.Allow}
	b, _ := json.Marshal(e)
	return b
}

func decodeResult(raw []byte) (identity.KeyResult, bool) {
	var e keyEntry
	if json.Unmarshal(raw, &e) != nil || !e.Valid {
		return identity.KeyResult{}, false
	}
	return identity.KeyResult{
		Status: identity.KeyValid,
		Caller: identity.Caller{ID: e.ID, Name: e.Name, Restricted: e.Restricted, Allow: e.Allow},
	}, true
}
