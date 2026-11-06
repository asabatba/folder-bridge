// Package update owns release trust, bounded staging, and installation recovery.
// It never reads pairing credentials or synchronization metadata.
package update

import (
	"archive/zip"
	"crypto/ed25519"
	"crypto/sha256"
	"encoding/base64"
	"encoding/binary"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"strings"
)

const Repository = "asabatba/folder-bridge"
const UpdaterVersion = "1.0.0"
const MaxArchive = 64 << 20
const MaxExtracted = 128 << 20

// PublicKey is injected into BOTH binaries using -ldflags. Empty means disabled.
var PublicKey string
var Version = "3.3.0"

type Manifest struct {
	Version               string `json:"version"`
	Platform              string `json:"platform"`
	URL                   string `json:"url"`
	SHA256                string `json:"sha256"`
	MinimumUpdaterVersion string `json:"minimumUpdaterVersion"`
}

// Stable versions deliberately exclude prereleases, build metadata and leading v.
func ParseVersion(s string) ([3]uint64, error) {
	var v [3]uint64
	parts := strings.Split(s, ".")
	if len(parts) != 3 {
		return v, errors.New("expected stable semantic version X.Y.Z")
	}
	for i, p := range parts {
		if p == "" || (len(p) > 1 && p[0] == '0') {
			return v, errors.New("invalid semantic version")
		}
		for _, c := range p {
			if c < '0' || c > '9' {
				return v, errors.New("invalid stable version")
			}
		}
		n, err := strconv.ParseUint(p, 10, 64)
		if err != nil {
			return v, err
		}
		v[i] = n
	}
	return v, nil
}
func Compare(a, b string) (int, error) {
	av, err := ParseVersion(a)
	if err != nil {
		return 0, err
	}
	bv, err := ParseVersion(b)
	if err != nil {
		return 0, err
	}
	for i := range av {
		if av[i] > bv[i] {
			return 1, nil
		}
		if av[i] < bv[i] {
			return -1, nil
		}
	}
	return 0, nil
}
func Key() (ed25519.PublicKey, error) {
	b, err := base64.StdEncoding.DecodeString(PublicKey)
	if err != nil || len(b) != ed25519.PublicKeySize {
		return nil, errors.New("updates unavailable: no valid embedded release public key; upgrade manually from https://github.com/" + Repository + "/releases")
	}
	return ed25519.PublicKey(b), nil
}
func ArtifactURL(raw, version, name string) bool {
	u, err := url.Parse(raw)
	return err == nil && u.Scheme == "https" && u.Host == "github.com" && u.User == nil && u.RawQuery == "" && u.Fragment == "" && u.RawPath == "" && u.Path == "/"+Repository+"/releases/download/v"+version+"/"+name
}
func VerifyManifest(data, sig []byte, key ed25519.PublicKey) (Manifest, error) {
	var m Manifest
	if len(data) > 32<<10 || len(key) != ed25519.PublicKeySize || len(sig) != ed25519.SignatureSize || !ed25519.Verify(key, data, sig) {
		return m, errors.New("invalid release manifest signature")
	}
	if err := json.Unmarshal(data, &m); err != nil {
		return m, err
	}
	if _, err := ParseVersion(m.Version); err != nil {
		return m, err
	}
	if m.Platform != "windows-amd64" {
		return m, errors.New("incompatible update platform")
	}
	cmp, err := Compare(m.MinimumUpdaterVersion, UpdaterVersion)
	if err != nil || cmp > 0 {
		return m, errors.New("release requires a newer updater; upgrade manually")
	}
	hash, err := hex.DecodeString(m.SHA256)
	if err != nil || len(hash) != sha256.Size {
		return m, errors.New("invalid release SHA-256")
	}
	if !ArtifactURL(m.URL, m.Version, "FolderBridge-v"+m.Version+"-Windows.zip") {
		return m, errors.New("untrusted release artifact URL")
	}
	return m, nil
}
func VerifyArchive(filename string, m Manifest) error {
	f, err := os.Open(filename)
	if err != nil {
		return err
	}
	defer f.Close()
	h := sha256.New()
	n, err := io.Copy(h, io.LimitReader(f, MaxArchive+1))
	if err != nil {
		return err
	}
	if n > MaxArchive || !strings.EqualFold(hex.EncodeToString(h.Sum(nil)), m.SHA256) {
		return errors.New("release archive SHA-256 mismatch or size exceeded")
	}
	return nil
}

// VerifyStage is shared by the main app, helper, and crash recovery. No local
// journal hash can substitute for the embedded signing key and signed ZIP hash.
func VerifyStage(dir string) (Manifest, error) {
	var zero Manifest
	if err := SafePath(dir); err != nil {
		return zero, err
	}
	read := func(name string, max int64) ([]byte, error) {
		path := filepath.Join(dir, name)
		if err := SafePath(path); err != nil {
			return nil, err
		}
		f, err := os.Open(path)
		if err != nil {
			return nil, err
		}
		defer f.Close()
		b, err := io.ReadAll(io.LimitReader(f, max+1))
		if err != nil {
			return nil, err
		}
		if int64(len(b)) > max {
			return nil, errors.New("staged release metadata is oversized")
		}
		return b, nil
	}
	data, err := read("manifest.json", 32<<10)
	if err != nil {
		return zero, err
	}
	sig, err := read("manifest.sig", 64)
	if err != nil {
		return zero, err
	}
	key, err := Key()
	if err != nil {
		return zero, err
	}
	m, err := VerifyManifest(data, sig, key)
	if err != nil {
		return zero, err
	}
	archive := filepath.Join(dir, "release.zip")
	if err = SafePath(archive); err != nil {
		return zero, err
	}
	if err = VerifyArchive(archive, m); err != nil {
		return zero, err
	}
	return m, nil
}

// Extract allows exactly two root-level regular executables; no directories,
// aliases, duplicate entries, ADS paths, symlinks, or arbitrary payload files.
// The caller supplies a freshly created private directory.
func Extract(archive, dir string) error {
	z, err := zip.OpenReader(archive)
	if err != nil {
		return err
	}
	defer z.Close()
	if len(z.File) != 2 {
		return errors.New("release ZIP must contain exactly two executables")
	}
	seen := map[string]bool{}
	var total uint64
	for _, entry := range z.File {
		if (entry.Name != "FolderBridge.exe" && entry.Name != "FolderBridge-Updater.exe") || seen[entry.Name] || !entry.Mode().IsRegular() {
			return fmt.Errorf("unsafe or unexpected ZIP entry %q", entry.Name)
		}
		seen[entry.Name] = true
		total += entry.UncompressedSize64
		if entry.UncompressedSize64 < 2 || entry.UncompressedSize64 > MaxExtracted/2 || total > MaxExtracted {
			return errors.New("excessive ZIP extraction size")
		}
		if entry.Method != zip.Store && entry.Method != zip.Deflate {
			return errors.New("unsupported ZIP compression")
		}
	}
	for _, entry := range z.File {
		if err := extractFile(entry, filepath.Join(dir, entry.Name)); err != nil {
			return err
		}
	}
	return nil
}
func extractFile(entry *zip.File, dest string) error {
	in, err := entry.Open()
	if err != nil {
		return err
	}
	defer in.Close()
	out, err := os.OpenFile(dest, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0700)
	if err != nil {
		return err
	}
	ok := false
	defer func() {
		out.Close()
		if !ok {
			os.Remove(dest)
		}
	}()
	n, err := io.Copy(out, io.LimitReader(in, MaxExtracted/2+1))
	if err != nil {
		return err
	}
	if uint64(n) != entry.UncompressedSize64 || n > MaxExtracted/2 {
		return errors.New("ZIP entry size mismatch")
	}
	if err = out.Sync(); err != nil {
		return err
	}
	if err = out.Close(); err != nil {
		return err
	}
	f, err := os.Open(dest)
	if err != nil {
		return err
	}
	var dos [64]byte
	_, err = io.ReadFull(f, dos[:])
	if err != nil || string(dos[:2]) != "MZ" {
		f.Close()
		return errors.New("release entry is not a Windows executable")
	}
	offset := int64(binary.LittleEndian.Uint32(dos[60:]))
	var pe [26]byte
	if offset < 64 || offset > 1<<20 {
		f.Close()
		return errors.New("invalid PE header offset")
	}
	_, err = f.ReadAt(pe[:], offset)
	f.Close()
	if err != nil || string(pe[:4]) != "PE\x00\x00" || binary.LittleEndian.Uint16(pe[4:]) != 0x8664 || binary.LittleEndian.Uint16(pe[24:]) != 0x20b {
		return errors.New("release entry is not a Windows x64 PE executable")
	}
	characteristics := binary.LittleEndian.Uint16(pe[22:])
	if characteristics&0x2 == 0 || characteristics&0x2000 != 0 {
		return errors.New("release entry is not an executable application")
	}
	ok = true
	return nil
}
