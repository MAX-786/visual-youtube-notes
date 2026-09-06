// Package ai holds the Processing role configuration (ADR 0010), the spend
// ledger writes (ADR 0007), and the OpenAI clients every role runs on
// (ADR 0017). Provider keys are sealed to the row that holds them and are
// never logged, returned, or placed in an error.
package ai

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"errors"
	"fmt"
	"strconv"

	"github.com/google/uuid"

	"github.com/MAX-786/visual-youtube-notes/backend/internal/keys"
)

// cipherPurpose derives the AES key from a master key, so the token HMAC key
// and this cipher key are independent though one file holds both.
const cipherPurpose = "provider-key-cipher"

// minKeyLength is the shortest secret Seal accepts: a real provider key is
// far longer, and the four-character hint must leave something behind.
const minKeyLength = 8

// Errors of sealing and opening.
var (
	ErrKeyTooShort       = errors.New("api key is too short")
	ErrKeyVersionMissing = errors.New("master key version is not in the key file")
	ErrCiphertextInvalid = errors.New("provider key ciphertext does not authenticate")
)

// Sealed is a provider key encrypted under one master key version: 12-byte
// nonce, ciphertext, 16-byte tag, in one value, with the version that
// unseals it and the hint Settings shows.
type Sealed struct {
	Ciphertext []byte
	Version    int
	Hint       string
}

// aad binds a ciphertext to its row: (user_id, role, key_version), never
// stored, reconstructed at decrypt, so a ciphertext moved between rows fails
// authentication instead of quietly decrypting (ADR 0010).
func aad(userID uuid.UUID, role string, version int) []byte {
	return []byte(userID.String() + "|" + role + "|" + strconv.Itoa(version))
}

func gcm(k keys.Key) (cipher.AEAD, error) {
	block, err := aes.NewCipher(keys.Derive(k, cipherPurpose))
	if err != nil {
		return nil, err
	}
	return cipher.NewGCM(block)
}

// Seal encrypts secret under the current master key for the row (userID, role).
func Seal(ring *keys.Ring, userID uuid.UUID, role, secret string) (Sealed, error) {
	runes := []rune(secret)
	if len(runes) < minKeyLength {
		return Sealed{}, ErrKeyTooShort
	}
	k := ring.Current()
	aead, err := gcm(k)
	if err != nil {
		return Sealed{}, err
	}
	nonce := make([]byte, aead.NonceSize())
	if _, err := rand.Read(nonce); err != nil {
		return Sealed{}, fmt.Errorf("nonce: %w", err)
	}
	out := aead.Seal(nonce, nonce, []byte(secret), aad(userID, role, k.Version))
	return Sealed{Ciphertext: out, Version: k.Version, Hint: string(runes[len(runes)-4:])}, nil
}

// Open decrypts a sealed key for the row (userID, role). A ciphertext from
// another row, another version or a tampered column fails with
// ErrCiphertextInvalid.
func Open(ring *keys.Ring, userID uuid.UUID, role string, version int, ciphertext []byte) (string, error) {
	k, ok := ring.ByVersion(version)
	if !ok {
		return "", fmt.Errorf("%w: version %d", ErrKeyVersionMissing, version)
	}
	aead, err := gcm(k)
	if err != nil {
		return "", err
	}
	if len(ciphertext) < aead.NonceSize()+aead.Overhead() {
		return "", ErrCiphertextInvalid
	}
	nonce, body := ciphertext[:aead.NonceSize()], ciphertext[aead.NonceSize():]
	plain, err := aead.Open(nil, nonce, body, aad(userID, role, version))
	if err != nil {
		return "", ErrCiphertextInvalid
	}
	return string(plain), nil
}
