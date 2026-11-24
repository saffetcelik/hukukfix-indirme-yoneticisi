// updsign makes and checks the signatures the auto-updater requires (see internal/update).
//
//	go run ./tools/updsign keygen <key-file>                   new key pair: writes the private key, prints the public key
//	go run ./tools/updsign sign <key-file> <version> <exe> <sig> writes the signed manifest for one release
//	go run ./tools/updsign verify <exe> <sig> [version]         checks a release against the key built into the program
//
// The private key file never belongs in the repository. Keep it (and a backup) somewhere private: without it no
// further update can be delivered, because the program installs only what this key has signed.
package main

import (
	"crypto/ed25519"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"strings"

	"hukukfixdm/internal/update"
)

func main() {
	if err := run(os.Args[1:]); err != nil {
		fmt.Fprintln(os.Stderr, "updsign:", err)
		os.Exit(1)
	}
}

func run(args []string) error {
	if len(args) == 0 {
		return errors.New("usage: keygen <key-file> | sign <key-file> <version> <exe> <sig> | verify <exe> <sig> [version]")
	}
	switch args[0] {
	case "keygen":
		if len(args) != 2 {
			return errors.New("usage: keygen <key-file>")
		}
		if _, err := os.Stat(args[1]); err == nil {
			return fmt.Errorf("%s already exists; refusing to overwrite a signing key", args[1])
		}
		pub, priv, err := ed25519.GenerateKey(rand.Reader)
		if err != nil {
			return err
		}
		seed := base64.StdEncoding.EncodeToString(priv.Seed())
		if err := os.WriteFile(args[1], []byte(seed+"\n"), 0o600); err != nil {
			return err
		}
		fmt.Println(base64.StdEncoding.EncodeToString(pub))
		return nil
	case "sign":
		if len(args) != 5 {
			return errors.New("usage: sign <key-file> <version> <exe> <sig>")
		}
		priv, err := readKey(args[1])
		if err != nil {
			return err
		}
		exe, err := os.ReadFile(args[3])
		if err != nil {
			return err
		}
		m := update.Sign(priv, args[2], exe)
		data, _ := json.MarshalIndent(m, "", "  ")
		if err := os.WriteFile(args[4], append(data, '\n'), 0o644); err != nil {
			return err
		}
		pub := base64.StdEncoding.EncodeToString(priv.Public().(ed25519.PublicKey))
		if pub != update.PublicKey {
			fmt.Fprintln(os.Stderr, "updsign: WARNING: this key is not the one built into the program; its updates would be rejected")
		}
		fmt.Printf("signed %s %s (%d bytes, sha256 %s)\n", args[3], m.Version, m.Size, m.SHA256)
		return nil
	case "verify":
		if len(args) != 3 && len(args) != 4 {
			return errors.New("usage: verify <exe> <sig> [version]")
		}
		exe, err := os.ReadFile(args[1])
		if err != nil {
			return err
		}
		raw, err := os.ReadFile(args[2])
		if err != nil {
			return err
		}
		var m update.Manifest
		if err := json.Unmarshal(raw, &m); err != nil {
			return err
		}
		if err := m.Verify(update.PublicKey); err != nil {
			return err
		}
		sum := sha256.Sum256(exe)
		if err := m.Matches(int64(len(exe)), hex.EncodeToString(sum[:])); err != nil {
			return err
		}
		if len(args) == 4 && update.Clean(args[3]) != m.Version {
			return fmt.Errorf("signed version %s, expected %s", m.Version, update.Clean(args[3]))
		}
		fmt.Printf("ok: %s is the signed %s\n", args[1], m.Version)
		return nil
	}
	return fmt.Errorf("unknown command %q", args[0])
}

func readKey(path string) (ed25519.PrivateKey, error) {
	raw, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	seed, err := base64.StdEncoding.DecodeString(strings.TrimSpace(string(raw)))
	if err != nil || len(seed) != ed25519.SeedSize {
		return nil, errors.New("not a signing key file")
	}
	return ed25519.NewKeyFromSeed(seed), nil
}
