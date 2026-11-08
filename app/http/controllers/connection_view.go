package controllers

import (
	"strings"

	"github.com/tidwall/gjson"

	"rllmx/app/repository"
)

// viewFields are the stored data fields a dashboard may see, in the order they
// are written. A field added to the row later stays hidden until it is listed
// here, and the credentials (apiKey, accessToken, refreshToken, idToken) are
// not, and never will be.
var viewFields = []string{
	"displayName", "globalPriority", "defaultModel", "testStatus", "lastTested", "lastError", "lastErrorAt",
	"errorCode", "rateLimitedUntil", "backoffLevel", "consecutiveUseCount", "lastUsedAt", "expiresAt",
	"tokenType", "scope", "projectId", "expiresIn", "lastRefreshAt",
}

// secretish are the name fragments that mark a value as a credential.
var secretish = []string{"token", "secret", "key", "password", "passwd", "cookie", "credential", "authorization"}

const maskedValue = "***"

// connectionView is what a connection looks like on the wire.
func connectionView(c repository.Connection) map[string]any {
	if c.Corrupt {
		return map[string]any{"id": c.ID, "provider": c.Provider, "corrupt": true}
	}
	d := c.Doc()
	out := map[string]any{
		"id": c.ID, "provider": c.Provider, "authType": c.AuthType, "name": c.Name, "email": c.Email,
		"priority": c.Priority, "isActive": c.IsActive, "createdAt": c.CreatedAt, "updatedAt": c.UpdatedAt,
	}
	for _, k := range viewFields {
		if raw, ok := d.GetRaw(k); ok && gjson.Valid(raw) {
			out[k] = gjson.Parse(raw).Value()
		}
	}
	for _, k := range d.Keys() {
		if strings.HasPrefix(k, repository.LockPrefix) && !d.IsNull(k) {
			if raw, ok := d.GetRaw(k); ok {
				out[k] = gjson.Parse(raw).Value()
			}
		}
	}
	if raw, ok := d.GetRaw("providerSpecificData"); ok && gjson.Parse(raw).IsObject() {
		out["providerSpecificData"] = maskObject(gjson.Parse(raw).Value())
	}
	return out
}

// maskObject replaces the value of every credential-looking name, at any depth.
func maskObject(v any) any {
	switch x := v.(type) {
	case map[string]any:
		out := make(map[string]any, len(x))
		for k, val := range x {
			if looksSecret(k) {
				out[k] = maskedValue
				continue
			}
			out[k] = maskObject(val)
		}
		return out
	case []any:
		out := make([]any, len(x))
		for i, val := range x {
			out[i] = maskObject(val)
		}
		return out
	}
	return v
}

func looksSecret(name string) bool {
	lower := strings.ToLower(name)
	for _, frag := range secretish {
		if strings.Contains(lower, frag) {
			return true
		}
	}
	return false
}
