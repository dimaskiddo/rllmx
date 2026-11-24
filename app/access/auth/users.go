package auth

import (
	"context"
	"crypto/rand"
	"encoding/base64"
	"errors"
	"strings"
	"time"

	"rllmx/app/platform/text"
	"rllmx/app/platform/ttl"
	"rllmx/app/store/repository"
)

// The dashboard has two roles. An admin configures everything; a standard user
// sees only the API keys assigned to them and those keys' usage.
const (
	RoleAdmin = "admin"
	RoleUser  = "user"
)

// maxUsername is the longest username CleanUser accepts, in bytes.
const maxUsername = 64

// DefaultMaxUsers bounds the user list when no limit is configured. The server
// reads AUTH_MAX_USERS into the repository at wiring time.
const DefaultMaxUsers = 200

// Errors a user write reports. Each names the rule, never the value.
var (
	ErrUserNoStore = errors.New("auth: the store is not open")
	ErrUserName    = errors.New("auth: a username is 1 to 64 characters with no control characters")
	ErrUserEmail   = errors.New("auth: an email is empty or one address")
	ErrUserHash    = errors.New("auth: a user needs a password")
	ErrUserFull    = errors.New("auth: the user list is full")
	ErrUserTaken   = errors.New("auth: a user with that name or email exists")
	ErrUserMissing = errors.New("auth: no such user")
)

// User is one dashboard account. The hash is never sent to a browser.
type User struct {
	ID          string
	Username    string
	Email       string
	Hash        string
	Provisioned bool
	CreatedAt   string
}

// UserRepo reads and writes the user table. The list is small and bounded, so
// every lookup is a read of the whole table.
type UserRepo struct {
	db    func() *repository.Store
	limit int
}

// NewUserRepo builds the repository over the configuration database. A limit
// below one falls back to DefaultMaxUsers, so a test needs no configuration.
func NewUserRepo(db func() *repository.Store, limit int) *UserRepo {
	if limit < 1 {
		limit = DefaultMaxUsers
	}
	return &UserRepo{db: db, limit: limit}
}

func (r *UserRepo) store() *repository.Store {
	if r == nil || r.db == nil {
		return nil
	}
	return r.db()
}

// List returns every user, ordered by username.
func (r *UserRepo) List(ctx context.Context) ([]User, error) {
	st := r.store()
	if st == nil {
		return nil, ErrUserNoStore
	}
	rows, err := st.Users().List(ctx)
	if err != nil {
		return nil, err
	}
	out := make([]User, 0, len(rows))
	for _, row := range rows {
		out = append(out, userOf(row))
	}
	return out, nil
}

func userOf(row repository.UserRow) User {
	return User{ID: row.ID, Username: row.Username, Email: row.Email, Hash: row.Password, Provisioned: row.Provisioned, CreatedAt: row.CreatedAt}
}

// Get returns one user by id.
func (r *UserRepo) Get(ctx context.Context, id string) (User, bool, error) {
	users, err := r.List(ctx)
	if err != nil {
		return User{}, false, err
	}
	for _, u := range users {
		if u.ID == id {
			return u, true, nil
		}
	}
	return User{}, false, nil
}

// Put creates or updates a user. A username and an email are unique across the
// list, and a new row is refused once the list is full.
func (r *UserRepo) Put(ctx context.Context, u User) error {
	st := r.store()
	if st == nil {
		return ErrUserNoStore
	}
	name, email, err := CleanUser(u.Username, u.Email)
	if err != nil {
		return err
	}
	if u.Hash == "" || u.ID == "" {
		return ErrUserHash
	}
	users, err := r.List(ctx)
	if err != nil {
		return err
	}
	fresh := true
	for _, other := range users {
		if other.ID == u.ID {
			fresh = false
			continue
		}
		if strings.EqualFold(other.Username, name) || (email != "" && strings.EqualFold(other.Email, email)) {
			return ErrUserTaken
		}
	}
	u.Username, u.Email = name, email
	if fresh {
		if u.CreatedAt == "" {
			u.CreatedAt = time.Now().UTC().Format(time.RFC3339)
		}
		row := repository.UserRow{ID: u.ID, Username: u.Username, Email: u.Email, Password: u.Hash, Provisioned: u.Provisioned, CreatedAt: u.CreatedAt}
		if err := st.Users().CreateWithin(ctx, row, r.limit); err != nil {
			if errors.Is(err, repository.ErrFull) {
				return ErrUserFull
			}
			return err
		}
		return nil
	}
	patch := repository.UserPatch{Username: &u.Username, Email: &u.Email, Password: &u.Hash, Provisioned: &u.Provisioned}
	if _, err := st.Users().Update(ctx, u.ID, patch); err != nil {
		if errors.Is(err, repository.ErrNotFound) {
			return ErrUserMissing
		}
		return err
	}
	return nil
}

// Delete removes a user and every key assignment that named them, which ends
// their session at the next request. The keys themselves stay.
func (r *UserRepo) Delete(ctx context.Context, id string) error {
	st := r.store()
	if st == nil {
		return ErrUserNoStore
	}
	removed, err := st.Users().Delete(ctx, id)
	if err != nil {
		return err
	}
	if !removed {
		return ErrUserMissing
	}
	return nil
}

// SetOwner assigns a key to a user. An empty user id clears the assignment.
func (r *UserRepo) SetOwner(ctx context.Context, keyID, userID string) error {
	st := r.store()
	if st == nil {
		return ErrUserNoStore
	}
	if userID == "" {
		return st.Users().ClearOwner(ctx, keyID)
	}
	if _, ok, err := r.Get(ctx, userID); err != nil {
		return err
	} else if !ok {
		return ErrUserMissing
	}
	return st.Users().SetOwner(ctx, keyID, userID)
}

// Owners maps every assigned key id to its user.
func (r *UserRepo) Owners(ctx context.Context) (map[string]string, error) {
	st := r.store()
	if st == nil {
		return nil, ErrUserNoStore
	}
	return st.Users().Owners(ctx)
}

// KeysOf returns the ids of the keys assigned to a user, sorted.
func (r *UserRepo) KeysOf(ctx context.Context, userID string) ([]string, error) {
	st := r.store()
	if st == nil {
		return nil, ErrUserNoStore
	}
	return st.Users().KeysOf(ctx, userID)
}

// CleanUser trims and checks a user's username and email. The email is stored
// lower-case, so the unique index cannot admit a second spelling of one address.
func CleanUser(username, email string) (string, string, error) {
	username, email = strings.TrimSpace(username), strings.ToLower(strings.TrimSpace(email))
	if username == "" || len(username) > maxUsername || text.HasControl(username) || strings.Contains(username, "/") {
		return "", "", ErrUserName
	}
	if email != "" && (len(email) > 320 || strings.Count(email, "@") != 1 || text.HasControl(email) || strings.ContainsAny(email, " \t")) {
		return "", "", ErrUserEmail
	}
	return username, email, nil
}

// NewUserID draws six lower-case characters, the alphabet the API key ids use,
// so both ids read the same way.
func NewUserID() (string, error) {
	const alphabet = "abcdefghijklmnopqrstuvwxyz0123456789"
	const limit = 256 / len(alphabet) * len(alphabet)
	out := make([]byte, 0, 6)
	buf := make([]byte, 1)
	for len(out) < 6 {
		if _, err := rand.Read(buf); err != nil {
			return "", err
		}
		if int(buf[0]) < limit {
			out = append(out, alphabet[int(buf[0])%len(alphabet)])
		}
	}
	return string(out), nil
}

// NewUserPassword draws 32 URL-safe characters, for an account provisioned by
// single sign-on. The value is hashed and dropped, so nobody ever knows it and
// password login waits for the user to set one.
func NewUserPassword() (string, error) {
	buf := make([]byte, 24)
	if _, err := rand.Read(buf); err != nil {
		return "", err
	}
	return base64.RawURLEncoding.EncodeToString(buf), nil
}

const usersTTL = 5 * time.Second

// Users is the user list every request path reads, reloaded every few seconds.
// A failed read answers nothing, so a session it cannot verify is not a session.
type Users struct {
	repo   *UserRepo
	now    func() time.Time
	cached *ttl.Value[[]User]
}

// NewUsers builds the reader.
func NewUsers(repo *UserRepo) *Users {
	u := &Users{repo: repo, now: time.Now}
	u.cached = ttl.New(usersTTL, func() time.Time { return u.now() }, func(ctx context.Context) ([]User, error) {
		return repo.List(ctx)
	})
	return u
}

// Changed drops the cache for a write this process just made.
func (u *Users) Changed() { u.cached.Drop() }

// Read returns every user and says when the list cannot be read, for the one
// caller that must not mistake a failure for an empty list.
func (u *Users) Read(ctx context.Context) ([]User, error) {
	if u == nil {
		return nil, ErrUserNoStore
	}
	return u.cached.Get(ctx, settingsLoadTimeout)
}

// All returns every user, or none when the list cannot be read.
func (u *Users) All(ctx context.Context) []User {
	if u == nil {
		return nil
	}
	list, err := u.cached.Get(ctx, settingsLoadTimeout)
	if err != nil {
		return nil
	}
	return list
}

// Get returns one user by id.
func (u *Users) Get(ctx context.Context, id string) (User, bool) {
	for _, v := range u.All(ctx) {
		if v.ID == id {
			return v, true
		}
	}
	return User{}, false
}

// ByUsername finds a user by name, ignoring case.
func (u *Users) ByUsername(ctx context.Context, name string) (User, bool) {
	for _, v := range u.All(ctx) {
		if strings.EqualFold(v.Username, name) {
			return v, true
		}
	}
	return User{}, false
}

// ByEmail finds a user by email, ignoring case.
func (u *Users) ByEmail(ctx context.Context, email string) (User, bool) {
	if email == "" {
		return User{}, false
	}
	for _, v := range u.All(ctx) {
		if v.Email != "" && strings.EqualFold(v.Email, email) {
			return v, true
		}
	}
	return User{}, false
}

// Put stores a user and drops the list this process reads.
func (u *Users) Put(ctx context.Context, v User) error {
	if u == nil {
		return ErrUserNoStore
	}
	if err := u.repo.Put(ctx, v); err != nil {
		return err
	}
	u.Changed()
	return nil
}

// Delete removes a user, their key assignments, and their sessions.
func (u *Users) Delete(ctx context.Context, id string) error {
	if u == nil {
		return ErrUserNoStore
	}
	if err := u.repo.Delete(ctx, id); err != nil {
		return err
	}
	u.Changed()
	return nil
}

// SetOwner assigns a key to a user, or clears it with an empty id.
func (u *Users) SetOwner(ctx context.Context, keyID, userID string) error {
	if u == nil {
		return ErrUserNoStore
	}
	if err := u.repo.SetOwner(ctx, keyID, userID); err != nil {
		return err
	}
	u.Changed()
	return nil
}

// KeysOf returns the keys assigned to a user. Ownership is read live, so a new
// assignment shows up in the next request.
func (u *Users) KeysOf(ctx context.Context, userID string) ([]string, error) {
	if u == nil {
		return nil, ErrUserNoStore
	}
	return u.repo.KeysOf(ctx, userID)
}

// Owners maps each assigned key to its user.
func (u *Users) Owners(ctx context.Context) (map[string]string, error) {
	if u == nil {
		return nil, ErrUserNoStore
	}
	return u.repo.Owners(ctx)
}
