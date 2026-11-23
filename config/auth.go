package config

import (
	"rllmx/app/facades"
)

func init() {
	config := facades.Config()
	config.Add("auth", map[string]any{
		// The dashboard password accepted while no hash is stored. Empty means
		// the built-in default; a remote login on a public default is refused
		// until the password is changed.
		"initial_password": config.Env("AUTH_INITIAL_PASSWORD", ""),
		// Checksum secret of an API key. It must equal the 9router value for
		// keys migrated from it to keep validating.
		"api_key_secret": config.Env("AUTH_API_KEY_SECRET", ""),
		// Salt of the machine id embedded in a new key. Same rule as above.
		"machine_id_salt": config.Env("AUTH_MACHINE_ID_SALT", ""),
		// Force the dashboard cookie's Secure flag even on plain HTTP, for a
		// deployment whose TLS ends somewhere this process cannot see.
		"cookie_secure": config.Env("AUTH_COOKIE_SECURE", false),
		// Refuse to start while the dashboard password is still a public
		// default. The check runs once the store is open.
		"refuse_default_password": config.Env("AUTH_REFUSE_DEFAULT_PASSWORD", false),
		// Who signs in through the identity provider as the admin: exact
		// addresses or @domain. An address not listed is admitted as a
		// standard user.
		"sso_admin": config.Env("AUTH_SSO_ADMIN", ""),
		// The most standard users the list may hold, provisioned or created.
		"max_users": config.Env("AUTH_MAX_USERS", 200),
	})
}
