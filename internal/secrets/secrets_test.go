package secrets

import (
	"bytes"
	"log/slog"
	"testing"

	"github.com/google/uuid"
)

// TestBox is P1-227's acceptance: values round-trip, ciphertext never shows
// the plaintext, and nothing opens what it should not (BR-101, BR-129).
func TestBox(t *testing.T) {
	box := mustBox(t, 1)
	shop := uuid.New()
	secret := []byte("Mid-server-THIS-IS-THE-SECRET")

	sealed, err := box.Seal(shop, secret)
	if err != nil {
		t.Fatal(err)
	}
	if got, err := box.Open(shop, sealed); err != nil || !bytes.Equal(got, secret) {
		t.Fatalf("round trip: %q %v", got, err)
	}
	again, _ := box.Seal(shop, secret)
	if bytes.Equal(sealed, again) {
		t.Error("two seals of one value are identical")
	}
	if bytes.Contains(sealed, secret) || bytes.Contains(sealed, secret[:12]) {
		t.Error("the ciphertext contains the plaintext")
	}

	tampered := append([]byte(nil), sealed...)
	tampered[len(tampered)-1] ^= 1
	for what, open := range map[string]func() ([]byte, error){
		"a tampered byte":   func() ([]byte, error) { return box.Open(shop, tampered) },
		"another shop":      func() ([]byte, error) { return box.Open(uuid.New(), sealed) },
		"another key":       func() ([]byte, error) { return mustBox(t, 2).Open(shop, sealed) },
		"a truncated value": func() ([]byte, error) { return box.Open(shop, sealed[:20]) },
		"nothing":           func() ([]byte, error) { return box.Open(shop, nil) },
	} {
		if got, err := open(); err != ErrCannotOpen || got != nil {
			t.Errorf("%s opened: %q %v", what, got, err)
		}
	}
	if _, err := New([]byte("short")); err == nil {
		t.Error("a short key was accepted")
	}
}

// TestBoxLogsNothing is the acceptance's log scan: sealing, opening and
// failing to open write no byte of the secret anywhere slog goes.
func TestBoxLogsNothing(t *testing.T) {
	var logs bytes.Buffer
	prev := slog.Default()
	slog.SetDefault(slog.New(slog.NewTextHandler(&logs, &slog.HandlerOptions{Level: slog.LevelDebug})))
	t.Cleanup(func() { slog.SetDefault(prev) })

	box, shop, secret := mustBox(t, 3), uuid.New(), []byte("Mid-server-LOG-SCAN-SECRET")
	sealed, _ := box.Seal(shop, secret)
	_, _ = box.Open(shop, sealed)
	_, _ = box.Open(uuid.New(), sealed)
	if bytes.Contains(logs.Bytes(), secret) || bytes.Contains(logs.Bytes(), sealed) {
		t.Errorf("the secret reached the log: %s", logs.String())
	}
}

func mustBox(t *testing.T, fill byte) *Box {
	t.Helper()
	box, err := New(bytes.Repeat([]byte{fill}, keySize))
	if err != nil {
		t.Fatal(err)
	}
	return box
}
