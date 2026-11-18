package controllers

import (
	"context"
	"encoding/json"
	"errors"
	nethttp "net/http"
	"strings"

	contractshttp "github.com/goravel/framework/contracts/http"

	"rllmx/app/llm/quota"
	"rllmx/app/store/repository"
)

// QuotaReader answers what is left of a connection's plan, and spends a refill
// when the dashboard asks. It is defined here so a test can fake it.
type QuotaReader interface {
	Read(ctx context.Context, conn repository.Connection, force bool) (quota.Reading, error)
	ClaudeReset(ctx context.Context, conn repository.Connection, grant string) (quota.ResetResult, error)
	CodexCredits(ctx context.Context, conn repository.Connection) (quota.ResetCredits, error)
	CodexConsume(ctx context.Context, conn repository.Connection) (quota.ConsumeResult, error)
}

// connection finds the connection a usage route names, answering the reference's
// 404 when it is not there.
func (c *UsageController) connection(ctx contractshttp.Context) (repository.Connection, contractshttp.Response) {
	st := c.store()
	if st == nil {
		return repository.Connection{}, storageDown(ctx)
	}
	conn, ok, err := st.Connections().Get(ctx.Request().Origin().Context(), ctx.Request().Route("id"))
	if err != nil {
		return repository.Connection{}, c.failed(ctx, "quota", "Failed to fetch usage", err)
	}
	if !ok {
		return repository.Connection{}, flat(ctx, nethttp.StatusNotFound, "Connection not found")
	}
	return conn, nil
}

func (c *UsageController) quotaReader() QuotaReader {
	if c.Quota == nil {
		return nil
	}
	return c.Quota()
}

// Connection answers GET /api/usage/{id} with the connection's plan readings.
func (c *UsageController) Connection(ctx contractshttp.Context) contractshttp.Response {
	conn, resp := c.connection(ctx)
	if resp != nil {
		return resp
	}
	q := c.quotaReader()
	if q == nil {
		return flat(ctx, nethttp.StatusInternalServerError, "Failed to fetch usage")
	}
	force := ctx.Request().Query("force") == "1"
	out, err := q.Read(ctx.Request().Origin().Context(), conn, force)
	switch {
	case errors.Is(err, quota.ErrUnsupported):
		return ctx.Response().Json(nethttp.StatusOK, contractshttp.Json{
			"message": "Usage API not implemented for " + conn.Provider,
		})
	case errors.Is(err, quota.ErrNotEligible):
		return ctx.Response().Json(nethttp.StatusOK, contractshttp.Json{
			"message": "Usage not available for this connection",
		})
	case errors.Is(err, quota.ErrExpired):
		return flat(ctx, nethttp.StatusUnauthorized, "Credential refresh failed: "+quotaReason(err))
	case err != nil:
		return c.failed(ctx, "quota", "Failed to fetch usage", err)
	}
	return ctx.Response().Json(nethttp.StatusOK, out)
}

// quotaReason is the reason a renewal failed, without the credential or a page
// of upstream text.
func quotaReason(err error) string {
	msg := strings.TrimSpace(err.Error())
	if i := strings.IndexByte(msg, ':'); i >= 0 {
		msg = strings.TrimSpace(msg[i+1:])
	}
	if len(msg) > 160 {
		msg = msg[:160]
	}
	if msg == "" {
		msg = "the token could not be renewed"
	}
	return msg
}

// claudeConnection checks that a reset route is asked about the right account.
func claudeConnection(ctx contractshttp.Context, conn repository.Connection) contractshttp.Response {
	if conn.Provider != "claude" || (conn.AuthType != "oauth" && conn.AuthType != "access_token") {
		return flat(ctx, nethttp.StatusBadRequest, "Limit reset is only available for Claude OAuth connections.")
	}
	return nil
}

// codexConnection checks that a reset-credit route is asked about a Codex login.
func codexConnection(ctx contractshttp.Context, conn repository.Connection) contractshttp.Response {
	if conn.Provider != "codex" {
		return flat(ctx, nethttp.StatusBadRequest, "Codex reset credits are only available for Codex connections.")
	}
	if conn.AuthType != "oauth" && conn.AuthType != "access_token" {
		return flat(ctx, nethttp.StatusBadRequest, "Codex reset credits require an OAuth or access-token connection.")
	}
	return nil
}

// ClaudeReset answers POST /api/usage/{id}/claude-reset: it spends one of the
// account's free refills, which cannot be undone.
func (c *UsageController) ClaudeReset(ctx contractshttp.Context) contractshttp.Response {
	conn, resp := c.connection(ctx)
	if resp != nil {
		return resp
	}
	if resp := claudeConnection(ctx, conn); resp != nil {
		return resp
	}
	q := c.quotaReader()
	if q == nil {
		return flat(ctx, nethttp.StatusInternalServerError, "Failed to reset limits")
	}
	var body struct {
		GrantID string `json:"grantId"`
	}
	_ = json.NewDecoder(ctx.Request().Origin().Body).Decode(&body)
	out, err := q.ClaudeReset(ctx.Request().Origin().Context(), conn, strings.TrimSpace(body.GrantID))
	switch {
	case errors.Is(err, quota.ErrInvalidGrant):
		return flat(ctx, nethttp.StatusBadRequest, "Invalid reset grant id.")
	case errors.Is(err, quota.ErrExpired):
		return flat(ctx, nethttp.StatusUnauthorized, "Credential refresh failed: "+quotaReason(err))
	case err != nil:
		return c.failed(ctx, "claude-reset", "Failed to reset limits", err)
	}
	if out.OK {
		return ctx.Response().Json(nethttp.StatusOK, out)
	}
	if out.Message == "" {
		reason := out.Reason
		if reason == "" {
			reason = out.Result
		}
		if reason == "" {
			reason = "unknown"
		}
		out.Message = "Reset not applied: " + reason
	}
	status := out.Status
	if status < 400 || status > 499 {
		status = nethttp.StatusConflict
	}
	return ctx.Response().Json(status, out)
}

// CodexCredits answers GET /api/usage/{id}/codex-reset-credits with the refills
// the account still holds.
func (c *UsageController) CodexCredits(ctx contractshttp.Context) contractshttp.Response {
	conn, resp := c.connection(ctx)
	if resp != nil {
		return resp
	}
	if resp := codexConnection(ctx, conn); resp != nil {
		return resp
	}
	q := c.quotaReader()
	if q == nil {
		return flat(ctx, nethttp.StatusInternalServerError, "Failed to fetch reset credits")
	}
	out, err := q.CodexCredits(ctx.Request().Origin().Context(), conn)
	switch {
	case errors.Is(err, quota.ErrAuth), errors.Is(err, quota.ErrExpired):
		return flat(ctx, nethttp.StatusUnauthorized, "Credential refresh failed: "+quotaReason(err))
	case errors.Is(err, quota.ErrRefused):
		return flat(ctx, nethttp.StatusInternalServerError, "Failed to fetch reset credits")
	case err != nil:
		return c.failed(ctx, "codex-credits", "Failed to fetch reset credits", err)
	}
	return ctx.Response().Json(nethttp.StatusOK, out)
}

// CodexResetCredits answers POST /api/usage/{id}/codex-reset-credits: it spends
// one credit and resets the windows the service names.
func (c *UsageController) CodexResetCredits(ctx contractshttp.Context) contractshttp.Response {
	conn, resp := c.connection(ctx)
	if resp != nil {
		return resp
	}
	if resp := codexConnection(ctx, conn); resp != nil {
		return resp
	}
	q := c.quotaReader()
	if q == nil {
		return flat(ctx, nethttp.StatusInternalServerError, "Failed to reset credits")
	}
	out, err := q.CodexConsume(ctx.Request().Origin().Context(), conn)
	switch {
	case errors.Is(err, quota.ErrAuth), errors.Is(err, quota.ErrExpired):
		return flat(ctx, nethttp.StatusUnauthorized, "Credential refresh failed: "+quotaReason(err))
	case err != nil:
		return c.failed(ctx, "codex-reset", "Failed to reset credits", err)
	}
	if out.Reset {
		return ctx.Response().Json(nethttp.StatusOK, out)
	}
	if out.Code == "no_credit" {
		if out.Message == "" {
			out.Message = "No Codex reset credits available."
		}
		return ctx.Response().Json(nethttp.StatusConflict, out)
	}
	if out.Message == "" {
		out.Message = "Codex reset credit consume returned an unexpected response."
	}
	status := out.Status
	if status < 400 || status > 499 {
		status = nethttp.StatusBadGateway
	}
	return ctx.Response().Json(status, out)
}
