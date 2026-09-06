// Package keys reads the application master key file (ADR 0010). The file
// holds versioned keys, newest first, so the current and previous key are
// both loadable during a rotation; provider keys are sealed under a specific
// version and token digests are keyed by one, so every version present in the
// database must be present here or the process refuses to start.
package keys

import (
	"bufio"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
	"errors"
	"fmt"
	"io"
	"os"
	"sort"
	"strconv"
	"strings"

	"golang.org/x/crypto/hkdf"
)

// Key is one 32-byte master key with its version.
type Key struct {
	Version int
	Secret  []byte
}

// Ring is every key the file holds.
type Ring struct {
	byVersion map[int]Key
	current   Key
}

// Load reads the key file. Each non-empty, non-comment line is
// "<version>:<base64 of 32 bytes>", for example "1:...". Versions must be
// positive and unique; the highest version is current.
func Load(path string) (*Ring, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, fmt.Errorf("open master key file: %w", err)
	}
	defer func() { _ = f.Close() }()
	return Parse(f)
}

// Parse reads a ring from key-file text.
func Parse(r io.Reader) (*Ring, error) {
	ring := &Ring{byVersion: map[int]Key{}}
	scanner := bufio.NewScanner(r)
	line := 0
	for scanner.Scan() {
		line++
		text := strings.TrimSpace(scanner.Text())
		if text == "" || strings.HasPrefix(text, "#") {
			continue
		}
		versionText, secretText, ok := strings.Cut(text, ":")
		if !ok {
			return nil, fmt.Errorf("master key line %d: expected <version>:<base64>", line)
		}
		version, err := strconv.Atoi(strings.TrimSpace(versionText))
		if err != nil || version <= 0 {
			return nil, fmt.Errorf("master key line %d: version must be a positive integer", line)
		}
		secret, err := decodeSecret(strings.TrimSpace(secretText))
		if err != nil {
			return nil, fmt.Errorf("master key line %d: %w", line, err)
		}
		if _, dup := ring.byVersion[version]; dup {
			return nil, fmt.Errorf("master key line %d: version %d appears twice", line, version)
		}
		ring.byVersion[version] = Key{Version: version, Secret: secret}
	}
	if err := scanner.Err(); err != nil {
		return nil, fmt.Errorf("read master key file: %w", err)
	}
	if len(ring.byVersion) == 0 {
		return nil, errors.New("master key file holds no keys")
	}
	versions := ring.Versions()
	ring.current = ring.byVersion[versions[len(versions)-1]]
	return ring, nil
}

// Current is the newest key, used for every new seal and digest.
func (r *Ring) Current() Key { return r.current }

// ByVersion finds a key for unsealing or verifying older material.
func (r *Ring) ByVersion(version int) (Key, bool) {
	k, ok := r.byVersion[version]
	return k, ok
}

// Versions lists the versions present, ascending.
func (r *Ring) Versions() []int {
	versions := make([]int, 0, len(r.byVersion))
	for v := range r.byVersion {
		versions = append(versions, v)
	}
	sort.Ints(versions)
	return versions
}

// Derive produces a purpose-specific 32-byte subkey from a master key with
// HKDF-SHA-256, so the token HMAC key and the provider-key cipher key are
// independent even though one file holds both (ADR 0003, ADR 0010).
func Derive(k Key, purpose string) []byte {
	out := make([]byte, 32)
	reader := hkdf.New(sha256.New, k.Secret, nil, []byte("vyn:"+purpose))
	if _, err := io.ReadFull(reader, out); err != nil {
		panic("hkdf: " + err.Error())
	}
	return out
}

// MAC computes HMAC-SHA-256 of data under a derived key.
func MAC(key, data []byte) []byte {
	m := hmac.New(sha256.New, key)
	m.Write(data)
	return m.Sum(nil)
}

func decodeSecret(text string) ([]byte, error) {
	for _, enc := range []*base64.Encoding{base64.StdEncoding, base64.RawStdEncoding, base64.URLEncoding, base64.RawURLEncoding} {
		secret, err := enc.DecodeString(text)
		if err == nil {
			if len(secret) != 32 {
				return nil, fmt.Errorf("key is %d bytes, expected 32", len(secret))
			}
			return secret, nil
		}
	}
	return nil, errors.New("key is not base64")
}
