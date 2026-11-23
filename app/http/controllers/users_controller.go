package controllers

import (
	"errors"
	nethttp "net/http"
	"sort"
	"strings"

	contractshttp "github.com/goravel/framework/contracts/http"
	"github.com/goravel/framework/contracts/log"

	"rllmx/app/access/auth"
)

// UsersController manages the dashboard's standard users. Every route is for
// the admin: the guard refuses them to a user role, and a user never sees
// another user's account.
type UsersController struct {
	Users *auth.Users
	Pass  auth.Passwords
	Log   log.Log
}

// userView is one account as the dashboard reads it: never the hash.
type userView struct {
	ID        string   `json:"id"`
	Username  string   `json:"username"`
	Email     string   `json:"email,omitempty"`
	CreatedAt string   `json:"createdAt"`
	Keys      []string `json:"keys"`
}

func (c *UsersController) storeDown(ctx contractshttp.Context) contractshttp.Response {
	if c.Users == nil {
		return storageDown(ctx)
	}
	return nil
}

// List answers GET /api/users with every account and the keys assigned to it.
func (c *UsersController) List(ctx contractshttp.Context) contractshttp.Response {
	if out := c.storeDown(ctx); out != nil {
		return out
	}
	rc := ctx.Request().Origin().Context()
	users := c.Users.All(rc)
	owners, err := c.Users.Owners(rc)
	if err != nil {
		return storageDown(ctx)
	}
	out := make([]userView, 0, len(users))
	for _, u := range users {
		view := userView{ID: u.ID, Username: u.Username, Email: u.Email, CreatedAt: u.CreatedAt, Keys: []string{}}
		for key, id := range owners {
			if id == u.ID {
				view.Keys = append(view.Keys, key)
			}
		}
		sort.Strings(view.Keys)
		out = append(out, view)
	}
	return ctx.Response().Json(nethttp.StatusOK, contractshttp.Json{"users": out})
}

// Create answers POST /api/users: a name, an optional email and a password.
func (c *UsersController) Create(ctx contractshttp.Context) contractshttp.Response {
	if out := c.storeDown(ctx); out != nil {
		return out
	}
	var b struct {
		Name     string `json:"name"`
		Email    string `json:"email"`
		Password string `json:"password"`
	}
	if err := bindJSON(ctx, &b); err != nil {
		return flat(ctx, nethttp.StatusBadRequest, "Invalid request body")
	}
	name, email, err := auth.CleanUser(b.Name, b.Email)
	if err != nil {
		return userRefused(ctx, err)
	}
	if err := auth.Judge(b.Password); err != nil {
		return flat(ctx, nethttp.StatusBadRequest, err.Error())
	}
	hash, err := c.Pass.Hash(b.Password)
	if err != nil {
		return flat(ctx, nethttp.StatusInternalServerError, "Could not store the password")
	}
	id, err := auth.NewUserID()
	if err != nil {
		return flat(ctx, nethttp.StatusInternalServerError, "Could not create the user")
	}
	u := auth.User{ID: id, Username: name, Email: email, Hash: hash}
	if err := c.Users.Put(ctx.Request().Origin().Context(), u); err != nil {
		return userRefused(ctx, err)
	}
	return ctx.Response().Json(nethttp.StatusCreated, contractshttp.Json{"user": userView{ID: u.ID, Username: u.Username, Email: u.Email, Keys: []string{}}})
}

// Update answers PUT /api/users/{id}: name, email and, when one is sent, a new
// password. A password reset ends that user's sessions and nobody else's.
func (c *UsersController) Update(ctx contractshttp.Context) contractshttp.Response {
	if out := c.storeDown(ctx); out != nil {
		return out
	}
	rc := ctx.Request().Origin().Context()
	u, ok := c.Users.Get(rc, ctx.Request().Route("id"))
	if !ok {
		return flat(ctx, nethttp.StatusNotFound, "User not found")
	}
	var b struct {
		Name     *string `json:"name"`
		Email    *string `json:"email"`
		Password string  `json:"password"`
	}
	if err := bindJSON(ctx, &b); err != nil {
		return flat(ctx, nethttp.StatusBadRequest, "Invalid request body")
	}
	if b.Name != nil {
		u.Username = *b.Name
	}
	if b.Email != nil {
		u.Email = *b.Email
	}
	if strings.TrimSpace(b.Password) != "" {
		if err := auth.Judge(b.Password); err != nil {
			return flat(ctx, nethttp.StatusBadRequest, err.Error())
		}
		hash, err := c.Pass.Hash(b.Password)
		if err != nil {
			return flat(ctx, nethttp.StatusInternalServerError, "Could not store the password")
		}
		u.Hash = hash
		u.Provisioned = false
	}
	if err := c.Users.Put(rc, u); err != nil {
		return userRefused(ctx, err)
	}
	return ctx.Response().Json(nethttp.StatusOK, contractshttp.Json{"user": userView{ID: u.ID, Username: u.Username, Email: u.Email}})
}

// Delete answers DELETE /api/users/{id}. The keys stay; the assignments go.
func (c *UsersController) Delete(ctx contractshttp.Context) contractshttp.Response {
	if out := c.storeDown(ctx); out != nil {
		return out
	}
	if err := c.Users.Delete(ctx.Request().Origin().Context(), ctx.Request().Route("id")); err != nil {
		return userRefused(ctx, err)
	}
	return ctx.Response().Json(nethttp.StatusOK, contractshttp.Json{"success": true})
}

// userRefused names the rule a write broke, without repeating a stored value.
func userRefused(ctx contractshttp.Context, err error) contractshttp.Response {
	switch {
	case errors.Is(err, auth.ErrUserMissing):
		return flat(ctx, nethttp.StatusNotFound, "User not found")
	case errors.Is(err, auth.ErrUserTaken):
		return flat(ctx, nethttp.StatusConflict, "That name or email is already used")
	case errors.Is(err, auth.ErrUserFull):
		return flat(ctx, nethttp.StatusConflict, "The user list is full")
	case errors.Is(err, auth.ErrUserName):
		return flat(ctx, nethttp.StatusBadRequest, "The name must be 1 to 64 bytes with no control character")
	case errors.Is(err, auth.ErrUserEmail):
		return flat(ctx, nethttp.StatusBadRequest, "The email must be one address")
	case errors.Is(err, auth.ErrUserHash):
		return flat(ctx, nethttp.StatusBadRequest, "A user needs a password")
	}
	return storageDown(ctx)
}
