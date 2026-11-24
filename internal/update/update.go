// Package update decides whether a GitHub release may replace the running program.
//
// A release carries two assets: HukukFixDM.exe and HukukFixDM.exe.sig. The .sig file is a small JSON manifest
// (version, size, SHA-256) signed with an Ed25519 key whose public half is built into the program. The private
// half never leaves the maintainer's machine, so a hijacked GitHub account, a tampered download or a mirror
// cannot push code to users: without a valid signature nothing is installed. Because the version is part of the
// signed text, an old (genuinely signed) release cannot be replayed as a "newer" one either.
package update

import (
	"crypto/ed25519"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"strconv"
	"strings"
)

const (
	Repo     = "saffetcelik/hukukfix-indirme-yoneticisi"
	AssetExe = "HukukFixDM.exe"
	AssetSig = "HukukFixDM.exe.sig"
	// ReleasesPage is the only address the "What's new" link may open.
	ReleasesPage = "https://github.com/" + Repo + "/releases/"
)

// These are variables only so the test build (-tags e2e) can point the updater at a local test server with a
// throwaway key. Release builds never change them.
var (
	// LatestURL answers with the newest published (non-draft, non-prerelease) release.
	LatestURL = "https://api.github.com/repos/" + Repo + "/releases/latest"
	// PublicKey verifies release manifests (base64, 32 bytes). See tools/updsign.
	PublicKey = publicKey
	// AllowHTTP lets the test build download from http://127.0.0.1.
	AllowHTTP = false
)

// Release is the part of the GitHub API answer the updater reads.
type Release struct {
	TagName    string  `json:"tag_name"`
	HTMLURL    string  `json:"html_url"`
	Draft      bool    `json:"draft"`
	Prerelease bool    `json:"prerelease"`
	Assets     []Asset `json:"assets"`
}

type Asset struct {
	Name string `json:"name"`
	Size int64  `json:"size"`
	URL  string `json:"browser_download_url"`
}

// Find returns the asset with the given name.
func (r Release) Find(name string) (Asset, bool) {
	for _, a := range r.Assets {
		if a.Name == name {
			return a, true
		}
	}
	return Asset{}, false
}

// Manifest is the content of HukukFixDM.exe.sig.
type Manifest struct {
	Version   string `json:"version"`
	Size      int64  `json:"size"`
	SHA256    string `json:"sha256"`
	Signature string `json:"signature"` // base64 Ed25519 signature of Message()
}

// Message is the exact text that is signed.
func (m Manifest) Message() []byte {
	return []byte("HukukFixDM update v1\nversion=" + m.Version + "\nsize=" + strconv.FormatInt(m.Size, 10) + "\nsha256=" + m.SHA256 + "\n")
}

var (
	ErrNoKey       = errors.New("no public key")
	ErrSignature   = errors.New("bad signature")
	ErrMismatch    = errors.New("file does not match the signed manifest")
	ErrNotNewer    = errors.New("signed version is not newer")
	ErrTagMismatch = errors.New("signed version differs from the release tag")
)

// Sign creates the manifest for exe. Only tools/updsign calls it.
func Sign(priv ed25519.PrivateKey, version string, exe []byte) Manifest {
	sum := sha256.Sum256(exe)
	m := Manifest{Version: Clean(version), Size: int64(len(exe)), SHA256: hex.EncodeToString(sum[:])}
	m.Signature = base64.StdEncoding.EncodeToString(ed25519.Sign(priv, m.Message()))
	return m
}

// Verify checks the signature with the given base64 public key.
func (m Manifest) Verify(pubB64 string) error {
	pub, err := base64.StdEncoding.DecodeString(pubB64)
	if err != nil || len(pub) != ed25519.PublicKeySize {
		return ErrNoKey
	}
	sig, err := base64.StdEncoding.DecodeString(m.Signature)
	if err != nil || len(sig) != ed25519.SignatureSize {
		return ErrSignature
	}
	if m.Version == "" || m.Size <= 0 || len(m.SHA256) != 64 || !ed25519.Verify(pub, m.Message(), sig) {
		return ErrSignature
	}
	return nil
}

// Accept is the whole decision for one release: the manifest must be signed by our key, name the same
// version as the release tag and be newer than the running program.
func (m Manifest) Accept(pubB64, tag, current string) error {
	if err := m.Verify(pubB64); err != nil {
		return err
	}
	if Clean(m.Version) != Clean(tag) {
		return ErrTagMismatch
	}
	if Compare(m.Version, current) <= 0 {
		return ErrNotNewer
	}
	return nil
}

// Matches reports whether a downloaded file is the signed one (size and SHA-256 hex digest).
func (m Manifest) Matches(size int64, sha256Hex string) error {
	if size != m.Size || !strings.EqualFold(sha256Hex, m.SHA256) {
		return ErrMismatch
	}
	return nil
}

// Clean turns "v1.2.0" into "1.2.0".
func Clean(v string) string {
	return strings.TrimPrefix(strings.TrimSpace(v), "v")
}

// Compare compares dotted versions numerically ("1.10.0" > "1.9.3"); a suffix such as "-beta" sorts before
// the plain version. Unparseable parts count as 0.
func Compare(a, b string) int {
	pa, sa := split(Clean(a))
	pb, sb := split(Clean(b))
	for i := 0; i < len(pa) || i < len(pb); i++ {
		var x, y int
		if i < len(pa) {
			x = pa[i]
		}
		if i < len(pb) {
			y = pb[i]
		}
		if x != y {
			if x < y {
				return -1
			}
			return 1
		}
	}
	switch {
	case sa == sb:
		return 0
	case sa == "":
		return 1
	case sb == "":
		return -1
	case sa < sb:
		return -1
	default:
		return 1
	}
}

func split(v string) ([]int, string) {
	suffix := ""
	if i := strings.IndexAny(v, "-+"); i >= 0 {
		v, suffix = v[:i], v[i+1:]
	}
	var out []int
	for _, p := range strings.Split(v, ".") {
		n, _ := strconv.Atoi(p)
		out = append(out, n)
	}
	return out, suffix
}
