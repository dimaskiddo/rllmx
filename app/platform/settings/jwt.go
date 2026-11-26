package settings

import (
	"errors"

	"github.com/goravel/framework/contracts/config"
)

// minJWTSecret is the shortest secret the session signer accepts.
const minJWTSecret = 32

// CheckJWT refuses a JWT_SECRET the signer would take but with too little
// entropy behind it. The secret names who may hold a session, so a short one
// stops the start rather than quietly weakening every sign-in.
func CheckJWT(cfg config.Config) error {
	if raw := cfg.GetString("jwt.secret"); raw != "" && len(raw) < minJWTSecret {
		return errors.New("settings: JWT_SECRET: must be at least 32 characters")
	}
	return nil
}
