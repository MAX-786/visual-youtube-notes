package keys

import (
	"bytes"
	"encoding/base64"
	"strings"
	"testing"
)

func TestParsePicksHighestVersionAsCurrent(t *testing.T) {
	one := base64.StdEncoding.EncodeToString(bytes.Repeat([]byte{1}, 32))
	two := base64.StdEncoding.EncodeToString(bytes.Repeat([]byte{2}, 32))
	ring, err := Parse(strings.NewReader("# rotated 2026-09-06\n2:" + two + "\n1:" + one + "\n"))
	if err != nil {
		t.Fatal(err)
	}
	if ring.Current().Version != 2 {
		t.Fatalf("current version %d", ring.Current().Version)
	}
	if _, ok := ring.ByVersion(1); !ok {
		t.Fatal("previous key missing")
	}
	if got := ring.Versions(); len(got) != 2 || got[0] != 1 || got[1] != 2 {
		t.Fatalf("versions %v", got)
	}
}

func TestParseRejectsBadKeys(t *testing.T) {
	cases := map[string]string{
		"empty":        "",
		"short":        "1:" + base64.StdEncoding.EncodeToString([]byte("short")),
		"no version":   base64.StdEncoding.EncodeToString(bytes.Repeat([]byte{1}, 32)),
		"duplicate":    "1:" + base64.StdEncoding.EncodeToString(bytes.Repeat([]byte{1}, 32)) + "\n1:" + base64.StdEncoding.EncodeToString(bytes.Repeat([]byte{2}, 32)),
		"zero version": "0:" + base64.StdEncoding.EncodeToString(bytes.Repeat([]byte{1}, 32)),
	}
	for name, text := range cases {
		if _, err := Parse(strings.NewReader(text)); err == nil {
			t.Errorf("%s: expected an error", name)
		}
	}
}

func TestDeriveIsPurposeSpecificAndStable(t *testing.T) {
	k := Key{Version: 1, Secret: bytes.Repeat([]byte{7}, 32)}
	a := Derive(k, "token-hmac")
	b := Derive(k, "provider-keys")
	if bytes.Equal(a, b) {
		t.Fatal("purposes derived the same key")
	}
	if !bytes.Equal(a, Derive(k, "token-hmac")) {
		t.Fatal("derivation is not deterministic")
	}
	if len(MAC(a, []byte("token"))) != 32 {
		t.Fatal("mac length")
	}
}
