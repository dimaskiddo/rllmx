package identity

import (
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"os"
	"path/filepath"
	"strings"

	"rllmx/app/file"
)

// DefaultMachineSalt is the salt a deployment gets without setting
// AUTH_MACHINE_ID_SALT. As with the key secret it is the reference's public
// value, so a migrated database keeps the same machine id.
const DefaultMachineSalt = "endpoint-proxy-salt"

const (
	machineFile = "machine-id"
	minRawBytes = 16
	maxRawBytes = 128
	// machineLen is how much of the hash is published: 16 hex characters.
	machineLen = 16
)

// ErrMachineID means no machine id could be read or made, so a key cannot be
// bound to this host.
var ErrMachineID = errors.New("identity: no machine id; start the server once so it can create one")

// Machine turns the raw machine id into the value a key carries.
type Machine struct {
	salt []byte
}

// NewMachine builds the machine-id codec. An empty salt uses the public default.
func NewMachine(salt string) Machine {
	if salt == "" {
		salt = DefaultMachineSalt
	}
	return Machine{salt: []byte(salt)}
}

// DefaultSalt reports whether the codec runs on the public default salt.
func (m Machine) DefaultSalt() bool { return string(m.salt) == DefaultMachineSalt }

// Load reads DATA_DIR/machine-id and returns its hashed form. ok is false when
// the file is missing or unusable; nothing is created here, because only the
// server writes this file.
func (m Machine) Load(dir string) (string, bool) {
	raw, err := readRaw(filepath.Join(dir, machineFile))
	if err != nil {
		return "", false
	}
	return m.hash(raw), true
}

// Ensure reads DATA_DIR/machine-id, creating it when missing: from
// /etc/machine-id when that is usable, else from crypto/rand. It is called by
// the server only, so a terminal command run as another user cannot leave a file
// the server cannot read.
func (m Machine) Ensure(dir string) (string, error) {
	path := filepath.Join(dir, machineFile)
	if raw, err := readRaw(path); err == nil {
		return m.hash(raw), nil
	}
	raw, err := systemMachineID()
	if err != nil {
		return "", err
	}
	if err := file.WritePrivate(dir, machineFile, []byte(raw)); err != nil {
		return "", err
	}
	return m.hash(raw), nil
}

// hash is first 16 hex of sha256(raw + salt), the format PRD 2 fixes.
func (m Machine) hash(raw string) string {
	sum := sha256.Sum256(append([]byte(raw), m.salt...))
	return hex.EncodeToString(sum[:])[:machineLen]
}

// readRaw reads and checks the raw id: the file is trimmed and must fit the
// length and alphabet PRD 2 states. A file that fails is never overwritten.
func readRaw(path string) (string, error) {
	b, err := os.ReadFile(path)
	if err != nil {
		return "", err
	}
	raw := strings.TrimSpace(string(b))
	if len(raw) < minRawBytes || len(raw) > maxRawBytes {
		return "", errors.New("identity: the machine id file is not a usable id")
	}
	for i := 0; i < len(raw); i++ {
		switch c := raw[i]; {
		case c >= 'a' && c <= 'z', c >= 'A' && c <= 'Z', c >= '0' && c <= '9', c == '-', c == '_', c == '.', c == ':':
		default:
			return "", errors.New("identity: the machine id file holds a character this format does not allow")
		}
	}
	return raw, nil
}

// systemMachineID returns /etc/machine-id when it is usable, else 16 random
// bytes in hex. Windows has no such file, so it always takes the random path.
func systemMachineID() (string, error) {
	if raw, err := readRaw("/etc/machine-id"); err == nil {
		return raw, nil
	}
	b := make([]byte, 16)
	if _, err := rand.Read(b); err != nil {
		return "", ErrKeyRandom
	}
	return hex.EncodeToString(b), nil
}
