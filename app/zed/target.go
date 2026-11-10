package zed

import (
	"context"
	"errors"

	"github.com/tidwall/gjson"

	"rllmx/app/repository"
)

// Targets answers the pipeline's question about a model's wire format for an
// account. It is a thin adapter so the pipeline holds an interface, not this
// package's client.
type Targets struct{ Client *Client }

// Format implements the pipeline's target resolver.
func (t Targets) Format(ctx context.Context, conn repository.Connection, model string) (string, error) {
	if t.Client == nil {
		return "", ErrUnknownTarget
	}
	d := conn.Doc()
	psd, _ := d.GetRaw("providerSpecificData")
	access, _ := d.GetString("accessToken")
	if access == "" {
		access, _ = d.GetString("apiKey")
	}
	a := Account{
		Access:         access,
		UserID:         stringField(psd, "userId", "zedUserId"),
		SystemID:       stringField(psd, "systemId", "zedSystemId"),
		OrganizationID: stringField(psd, "organizationId", "zedOrganizationId"),
	}
	format, err := t.Client.Format(ctx, a, model)
	if errors.Is(err, ErrUnknownTarget) {
		return "", err
	}
	return format, err
}

// stringField reads the first of several names a field may be stored under.
func stringField(raw string, names ...string) string {
	for _, n := range names {
		if v := gjson.Get(raw, n).String(); v != "" {
			return v
		}
	}
	return ""
}
