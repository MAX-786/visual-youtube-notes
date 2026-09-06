package ai

import (
	"errors"
	"strings"
	"testing"

	"github.com/google/uuid"

	"github.com/MAX-786/visual-youtube-notes/backend/internal/keys"
)

func testRing(t *testing.T, versions ...string) *keys.Ring {
	t.Helper()
	ring, err := keys.Parse(strings.NewReader(strings.Join(versions, "\n")))
	if err != nil {
		t.Fatal(err)
	}
	return ring
}

const (
	keyV1 = "1:AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA="
	keyV2 = "2:AQEBAQEBAQEBAQEBAQEBAQEBAQEBAQEBAQEBAQEBAQE="
)

func TestSealAndOpenRoundTrip(t *testing.T) {
	ring := testRing(t, keyV1)
	user := uuid.New()
	sealed, err := Seal(ring, user, RoleNoteGeneration, "sk-test-1234567890wxyz")
	if err != nil {
		t.Fatal(err)
	}
	if sealed.Version != 1 || sealed.Hint != "wxyz" || len(sealed.Ciphertext) < 29 {
		t.Fatalf("sealed %+v", sealed)
	}
	plain, err := Open(ring, user, RoleNoteGeneration, sealed.Version, sealed.Ciphertext)
	if err != nil || plain != "sk-test-1234567890wxyz" {
		t.Fatalf("open: %v %q", err, plain)
	}
}

func TestCiphertextMovedBetweenRolesFails(t *testing.T) {
	ring := testRing(t, keyV1)
	user := uuid.New()
	sealed, _ := Seal(ring, user, RoleTranscription, "sk-transcription-key")
	if _, err := Open(ring, user, RoleEmbeddings, sealed.Version, sealed.Ciphertext); !errors.Is(err, ErrCiphertextInvalid) {
		t.Fatalf("moved between roles: %v", err)
	}
	if _, err := Open(ring, uuid.New(), RoleTranscription, sealed.Version, sealed.Ciphertext); !errors.Is(err, ErrCiphertextInvalid) {
		t.Fatalf("moved between owners: %v", err)
	}
	tampered := append([]byte(nil), sealed.Ciphertext...)
	tampered[len(tampered)-1] ^= 1
	if _, err := Open(ring, user, RoleTranscription, sealed.Version, tampered); !errors.Is(err, ErrCiphertextInvalid) {
		t.Fatalf("tampered: %v", err)
	}
}

func TestRotationKeepsOldVersionsReadable(t *testing.T) {
	old := testRing(t, keyV1)
	user := uuid.New()
	sealed, _ := Seal(old, user, RoleEmbeddings, "sk-embeddings-key-abcd")
	rotated := testRing(t, keyV1, keyV2)
	if rotated.Current().Version != 2 {
		t.Fatal("newest version should be current")
	}
	plain, err := Open(rotated, user, RoleEmbeddings, 1, sealed.Ciphertext)
	if err != nil || plain != "sk-embeddings-key-abcd" {
		t.Fatalf("old version unreadable after rotation: %v", err)
	}
	if _, err := Open(rotated, user, RoleEmbeddings, 2, sealed.Ciphertext); !errors.Is(err, ErrCiphertextInvalid) {
		t.Fatalf("wrong version accepted: %v", err)
	}
	onlyNew := testRing(t, keyV2)
	if _, err := Open(onlyNew, user, RoleEmbeddings, 1, sealed.Ciphertext); !errors.Is(err, ErrKeyVersionMissing) {
		t.Fatalf("missing version: %v", err)
	}
}

func TestShortKeyRefused(t *testing.T) {
	if _, err := Seal(testRing(t, keyV1), uuid.New(), RoleEmbeddings, "short"); !errors.Is(err, ErrKeyTooShort) {
		t.Fatalf("short key: %v", err)
	}
}
