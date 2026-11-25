package controllers

import (
	"context"
	"strings"

	"rllmx/app/access/auth"
	"rllmx/app/platform/text"
	"rllmx/app/store/repository"
)

// usageKeyName is what an answer may say about a stored key: its name, and a
// mask for a key that carries none. Never the key.
type usageKeyName struct{ name, masked string }

// keyNames reads the stored keys once, so an answer can name and mask a key
// without ever holding the key itself. An unreadable store answers nothing.
func keyNames(ctx context.Context, st *repository.Store) map[string]usageKeyName {
	out := map[string]usageKeyName{}
	if st == nil {
		return out
	}
	keys, err := st.APIKeys().List(ctx)
	if err != nil {
		return out
	}
	for _, k := range keys {
		masked := auth.MaskKey(k.Key)
		name := k.Name
		if name == "" {
			name = masked
		}
		out[k.ID] = usageKeyName{name: name, masked: masked}
	}
	return out
}

// accountNames maps a connection to the name the usage page and the metrics
// endpoint show.
func accountNames(ctx context.Context, st *repository.Store) map[string]string {
	out := map[string]string{}
	if st == nil {
		return out
	}
	rows, err := st.Connections().List(ctx, repository.ConnFilter{})
	if err != nil {
		return out
	}
	for _, row := range rows {
		out[row.ID] = connectionAccount(row)
	}
	return out
}

// connectionAccount names one upstream account: its own name, its email, or a
// shortened id.
func connectionAccount(row repository.Connection) string {
	if name := strings.TrimSpace(row.Name); name != "" {
		return name
	}
	if email := strings.TrimSpace(row.Email); email != "" {
		return email
	}
	if row.ID == "" {
		return ""
	}
	if len(row.ID) <= 8 {
		return "Account " + row.ID
	}
	return "Account " + text.CutBytes(row.ID, 8) + "..."
}
