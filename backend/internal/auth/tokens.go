package auth

import (
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"errors"
	"fmt"

	"github.com/MAX-786/visual-youtube-notes/backend/internal/keys"
)

// tokenBytes is the size of every opaque credential: access, refresh, CSRF.
const tokenBytes = 32

// Keyring digests tokens under the master key ring. The digest key is derived
// per master-key version, so hash_key_id is the master key's version and a
// rotation needs no table of its own.
type Keyring struct {
	ring *keys.Ring
}

// NewKeyring wraps a master key ring.
func NewKeyring(ring *keys.Ring) *Keyring {
	return &Keyring{ring: ring}
}

// Digest is a token's HMAC under one key version.
type Digest struct {
	KeyID int
	Hash  []byte
}

// mintToken returns a fresh random credential and its presentation.
func mintToken() ([]byte, string, error) {
	raw := make([]byte, tokenBytes)
	if _, err := rand.Read(raw); err != nil {
		return nil, "", fmt.Errorf("random token: %w", err)
	}
	return raw, base64.RawURLEncoding.EncodeToString(raw), nil
}

// decodeToken turns a presented credential back into bytes. Anything that is
// not exactly 32 base64url bytes is refused before it reaches a lookup.
func decodeToken(presented string) ([]byte, error) {
	raw, err := base64.RawURLEncoding.DecodeString(presented)
	if err != nil || len(raw) != tokenBytes {
		return nil, errors.New("malformed token")
	}
	return raw, nil
}

// Current digests a token under the newest key.
func (k *Keyring) Current(raw []byte) Digest {
	current := k.ring.Current()
	return Digest{KeyID: current.Version, Hash: keys.MAC(keys.Derive(current, "token-hmac"), raw)}
}

// Candidates digests a token under every key, newest first, so a credential
// issued under the previous key still verifies during a rotation.
func (k *Keyring) Candidates(raw []byte) []Digest {
	versions := k.ring.Versions()
	out := make([]Digest, 0, len(versions))
	for i := len(versions) - 1; i >= 0; i-- {
		key, _ := k.ring.ByVersion(versions[i])
		out = append(out, Digest{KeyID: key.Version, Hash: keys.MAC(keys.Derive(key, "token-hmac"), raw)})
	}
	return out
}

// csrfHash is the stored form of a CSRF token: a plain SHA-256, since the
// token is non-secret and session-bound rather than a bearer credential.
func csrfHash(raw []byte) []byte {
	sum := sha256.Sum256(raw)
	return sum[:]
}

// CSRFHashOf hashes a presented CSRF token for comparison.
func CSRFHashOf(presented string) ([]byte, error) {
	raw, err := decodeToken(presented)
	if err != nil {
		return nil, err
	}
	return csrfHash(raw), nil
}
