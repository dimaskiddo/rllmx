package commands

import (
	"errors"
	"fmt"
	"strings"

	"github.com/goravel/framework/contracts/console"
	"github.com/goravel/framework/contracts/console/command"

	"rllmx/app/access/auth"
	"rllmx/app/access/oidc"
	"rllmx/app/access/saml"
	"rllmx/app/store/data"
	"rllmx/app/store/document"
)

// AuthMode sets how the dashboard is signed into, and may configure the OIDC
// client in the same write.
type AuthMode struct{ R *DatabaseRuntime }

func (c *AuthMode) Signature() string { return "auth:mode" }

func (c *AuthMode) Description() string {
	return "Set the dashboard sign-in mode; the password form stays on until a chosen single sign-on is configured"
}

func (c *AuthMode) Extend() command.Extend {
	return command.Extend{Category: "auth", Flags: []command.Flag{
		&command.StringFlag{Name: "mode", Usage: "password, oidc, saml, sso or both; sso and both use --sso-type"},
		&command.StringFlag{Name: "issuer", Usage: "OIDC issuer URL"},
		&command.StringFlag{Name: "client-id", Usage: "OIDC client id"},
		&command.StringFlag{Name: "sso-type", Usage: "oidc or saml, the type sso and both use"},
	}}
}

func (c *AuthMode) Handle(ctx console.Context) error {
	mode, err := c.mode(ctx)
	if err != nil {
		return err
	}
	in, err := readAuthFlags(ctx)
	if err != nil {
		return err
	}
	h, err := c.R.Open(ctx, data.ModeWrite)
	if err != nil {
		return err
	}
	cur, err := h.Store.Settings().Effective(ctx)
	if err != nil {
		return err
	}
	if in.ssoType == "" {
		in.ssoType, _ = cur.GetString("ssoType")
	}

	patch := map[string]string{"authMode": document.Encode(mode)}
	if in.issuer != "" {
		patch["oidcIssuerUrl"] = document.Encode(in.issuer)
	}
	if in.clientID != "" {
		patch["oidcClientId"] = document.Encode(in.clientID)
	}
	if in.ssoTypeSet {
		patch["ssoType"] = document.Encode(in.ssoType)
	}
	if auth.ResolveMode(mode, in.ssoType, true, false).OIDC {
		if err := c.oidcClient(ctx, in, cur, patch); err != nil {
			return err
		}
	}

	if err := h.Store.Settings().Patch(ctx, patch); err != nil {
		return err
	}
	after, err := h.Store.Settings().Effective(ctx)
	if err != nil {
		return err
	}
	report(ctx, mode, after)
	return nil
}

// authFlags are the options the command was given, trimmed and checked.
type authFlags struct {
	ssoType    string
	ssoTypeSet bool
	issuer     string
	clientID   string
}

// readAuthFlags reads and bounds every flag, and canonicalizes the issuer the
// way discovery will read it.
func readAuthFlags(ctx console.Context) (authFlags, error) {
	var in authFlags
	in.ssoType = strings.TrimSpace(ctx.Option("sso-type"))
	in.ssoTypeSet = in.ssoType != ""
	if in.ssoType != "" && in.ssoType != "oidc" && in.ssoType != "saml" {
		return in, errors.New("auth:mode: --sso-type is oidc or saml")
	}
	in.clientID = strings.TrimSpace(ctx.Option("client-id"))
	if len(in.clientID) > 512 {
		return in, errors.New("auth:mode: the client id is longer than 512 bytes")
	}
	in.issuer = strings.TrimSpace(ctx.Option("issuer"))
	if in.issuer == "" {
		return in, nil
	}
	if len(in.issuer) > 2048 {
		return in, errors.New("auth:mode: the issuer is longer than 2048 bytes")
	}
	issuer, err := oidc.ParseIssuer(in.issuer)
	if err != nil {
		return in, fmt.Errorf("auth:mode: %w", err)
	}
	in.issuer = issuer
	return in, nil
}

// oidcClient checks that the OIDC client is complete and adds its secret to the
// patch when one has to be asked for.
func (c *AuthMode) oidcClient(ctx console.Context, in authFlags, cur document.Doc, patch map[string]string) error {
	effIssuer := orString(in.issuer, cur, "oidcIssuerUrl")
	effID := orString(in.clientID, cur, "oidcClientId")
	if effIssuer == "" || effID == "" {
		return errors.New("auth:mode: OIDC needs --issuer and --client-id")
	}
	// A stored secret belongs to the client it was issued for. Moving to
	// another issuer or client id with the old one would turn the password
	// form off for a login whose token exchange can never succeed.
	secret, _ := cur.GetString("oidcClientSecret")
	if storedIssuer, _ := cur.GetString("oidcIssuerUrl"); storedIssuer != effIssuer {
		secret = ""
	} else if storedID, _ := cur.GetString("oidcClientId"); storedID != effID {
		secret = ""
	}
	if secret == "" {
		if !c.R.Terminal() {
			return errors.New("auth:mode needs a terminal to ask for the OIDC client secret")
		}
		var err error
		if secret, err = ctx.Secret("OIDC client secret"); err != nil {
			return err
		}
		if secret == "" || len(secret) > 512 {
			return errors.New("auth:mode: the OIDC client secret must be 1 to 512 bytes")
		}
		patch["oidcClientSecret"] = document.Encode(secret)
	}
	eff := oidc.Settings{
		Issuer: effIssuer, ClientID: effID, ClientSecret: secret,
		Scopes: orString("", cur, "oidcScopes"),
	}
	if err := eff.Validate(); err != nil {
		return fmt.Errorf("auth:mode: %w", err)
	}
	return nil
}

// report says what the sign-in page will offer and warns about the two states
// that leave an operator without the way in they expected.
func report(ctx console.Context, mode string, after document.Doc) {
	resolved := resolveMode(after)
	offered := make([]string, 0, 3)
	if resolved.Password {
		offered = append(offered, "password")
	}
	if resolved.OIDC {
		offered = append(offered, "OIDC")
	}
	if resolved.SAML {
		offered = append(offered, "SAML")
	}
	ctx.Line("sign-in offered: " + strings.Join(offered, ", "))
	if mode != "password" && !resolved.SSO() {
		ctx.Warning("single sign-on is not configured; the password form stays on until it is")
	}
	if resolved.Password {
		if hash, _ := after.GetString("password"); hash == "" {
			ctx.Warning("no dashboard password is set; the first sign-in must change the default one, or run password:reset")
		}
	}
}

// mode answers the flag or asks for it, and refuses a value the settings API
// would not take.
func (c *AuthMode) mode(ctx console.Context) (string, error) {
	mode := strings.TrimSpace(ctx.Option("mode"))
	if mode == "" {
		answer, err := ctx.Ask("Sign-in mode (password, oidc, saml, sso, both)")
		if err != nil {
			return "", err
		}
		mode = strings.TrimSpace(answer)
	}
	for _, allowed := range auth.ModeNames() {
		if mode == allowed {
			return mode, nil
		}
	}
	return "", fmt.Errorf("auth:mode: the mode must be one of %s", strings.Join(auth.ModeNames(), ", "))
}

// resolveMode is the sign-in decision the server makes from the stored row.
func resolveMode(d document.Doc) auth.Mode {
	authMode, _ := d.GetString("authMode")
	ssoType, _ := d.GetString("ssoType")
	return auth.ResolveMode(authMode, ssoType, oidc.FromDoc(d).Usable(), saml.FromDoc(d).Usable())
}

// orString returns the flag value, else the stored setting.
func orString(flag string, d document.Doc, key string) string {
	if flag != "" {
		return flag
	}
	v, _ := d.GetString(key)
	return v
}
