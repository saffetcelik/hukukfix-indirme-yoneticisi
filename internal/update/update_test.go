package update

import (
	"crypto/ed25519"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"testing"
)

func TestCompare(t *testing.T) {
	cases := []struct {
		a, b string
		want int
	}{
		{"1.2.0", "1.1.2", 1},
		{"v1.2.0", "1.2.0", 0},
		{"1.10.0", "1.9.9", 1},
		{"1.2", "1.2.0", 0},
		{"1.2.0-beta", "1.2.0", -1},
		{"2.0.0", "10.0.0", -1},
		{"", "1.0.0", -1},
	}
	for _, c := range cases {
		if got := Compare(c.a, c.b); got != c.want {
			t.Errorf("Compare(%q, %q) = %d, want %d", c.a, c.b, got, c.want)
		}
	}
}

func TestManifest(t *testing.T) {
	pub, priv, _ := ed25519.GenerateKey(rand.Reader)
	key := base64.StdEncoding.EncodeToString(pub)
	exe := []byte("pretend this is HukukFixDM.exe")
	m := Sign(priv, "v1.3.0", exe)

	if err := m.Accept(key, "v1.3.0", "1.2.0"); err != nil {
		t.Fatalf("valid release rejected: %v", err)
	}
	sum := sha256.Sum256(exe)
	if err := m.Matches(int64(len(exe)), hex.EncodeToString(sum[:])); err != nil {
		t.Fatalf("matching file rejected: %v", err)
	}

	// Another key, a replayed old release, a different tag and a changed file must all fail.
	otherPub, _, _ := ed25519.GenerateKey(rand.Reader)
	if err := m.Accept(base64.StdEncoding.EncodeToString(otherPub), "v1.3.0", "1.2.0"); err != ErrSignature {
		t.Errorf("foreign key: got %v", err)
	}
	if err := m.Accept(key, "v1.3.0", "1.3.0"); err != ErrNotNewer {
		t.Errorf("same version: got %v", err)
	}
	if err := m.Accept(key, "v9.9.9", "1.2.0"); err != ErrTagMismatch {
		t.Errorf("tag mismatch: got %v", err)
	}
	forged := m
	forged.Version = "9.9.9" // relabelled without re-signing
	if err := forged.Accept(key, "v9.9.9", "1.2.0"); err != ErrSignature {
		t.Errorf("relabelled manifest: got %v", err)
	}
	if err := m.Matches(int64(len(exe)), hex.EncodeToString(make([]byte, 32))); err != ErrMismatch {
		t.Errorf("changed file: got %v", err)
	}
	if err := m.Verify("not-a-key"); err != ErrNoKey {
		t.Errorf("bad key: got %v", err)
	}
}
