// Package auth is Owner authentication (ADR 0003): Argon2id passwords,
// server-held opaque token digests, Device sessions as the revocation
// boundary, atomic refresh rotation with reuse revocation, and the CSRF token
// bound to each session.
package auth

import (
	"crypto/rand"
	"crypto/subtle"
	"encoding/base64"
	"errors"
	"fmt"
	"strings"

	"golang.org/x/crypto/argon2"
)

// Argon2id parameters from ADR 0003: 64 MiB, 3 iterations, parallelism 1,
// a 16-byte salt and a 32-byte output. The encoded hash carries them so a
// later login can rehash after they increase.
const (
	argonMemory      = 64 * 1024
	argonIterations  = 3
	argonParallelism = 1
	argonSaltLength  = 16
	argonKeyLength   = 32
)

// HashPassword produces a PHC-encoded Argon2id hash.
func HashPassword(password string) (string, error) {
	salt := make([]byte, argonSaltLength)
	if _, err := rand.Read(salt); err != nil {
		return "", fmt.Errorf("salt: %w", err)
	}
	key := argon2.IDKey([]byte(password), salt, argonIterations, argonMemory, argonParallelism, argonKeyLength)
	return fmt.Sprintf("$argon2id$v=%d$m=%d,t=%d,p=%d$%s$%s",
		argon2.Version, argonMemory, argonIterations, argonParallelism,
		base64.RawStdEncoding.EncodeToString(salt),
		base64.RawStdEncoding.EncodeToString(key)), nil
}

// VerifyPassword checks a password against a PHC-encoded hash in constant
// time. It returns whether the hash was produced with parameters below the
// current ones, so the caller can rehash on success.
func VerifyPassword(encoded, password string) (ok, needsRehash bool, err error) {
	parts := strings.Split(encoded, "$")
	if len(parts) != 6 || parts[1] != "argon2id" {
		return false, false, errors.New("not an argon2id hash")
	}
	var version int
	if _, err := fmt.Sscanf(parts[2], "v=%d", &version); err != nil {
		return false, false, fmt.Errorf("version: %w", err)
	}
	var memory, iterations uint32
	var parallelism uint8
	if _, err := fmt.Sscanf(parts[3], "m=%d,t=%d,p=%d", &memory, &iterations, &parallelism); err != nil {
		return false, false, fmt.Errorf("parameters: %w", err)
	}
	salt, err := base64.RawStdEncoding.DecodeString(parts[4])
	if err != nil {
		return false, false, fmt.Errorf("salt: %w", err)
	}
	want, err := base64.RawStdEncoding.DecodeString(parts[5])
	if err != nil {
		return false, false, fmt.Errorf("hash: %w", err)
	}
	got := argon2.IDKey([]byte(password), salt, iterations, memory, parallelism, uint32(len(want)))
	ok = subtle.ConstantTimeCompare(got, want) == 1
	needsRehash = memory < argonMemory || iterations < argonIterations
	return ok, needsRehash, nil
}

// dummyHash is verified against when the username does not exist, so a
// missing Owner costs the same time as a wrong password.
var dummyHash = func() string {
	h, err := HashPassword("this password is never accepted")
	if err != nil {
		panic(err)
	}
	return h
}()
