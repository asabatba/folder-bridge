// Release tooling is deliberately separate from both shipped executables.
// Run keygen/sign only on the offline signing machine, never in CI.
package main

import (
	"archive/zip"
	"bytes"
	"crypto/ed25519"
	"crypto/rand"
	"crypto/sha256"
	"crypto/x509"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"encoding/pem"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"

	"folderbridge/internal/update"
)

func main() {
	if err := run(); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}
func run() error {
	mode := flag.String("mode", "verify", "keygen, package, sign, or verify")
	dir := flag.String("dir", "dist/release", "artifact directory")
	version := flag.String("version", "", "stable semantic version without v")
	private := flag.String("private-key", "", "offline PKCS#8 PEM private key file")
	public := flag.String("public-key", "", "base64 public key file (or UPDATE_PUBLIC_KEY environment variable)")
	signature := flag.String("signature-base64", "", "offline signature for publication; alternatively set UPDATE_SIGNATURE")
	flag.Parse()
	switch *mode {
	case "keygen":
		if *private == "" {
			return errors.New("provide --private-key on an offline machine")
		}
		pub, key, err := ed25519.GenerateKey(rand.Reader)
		if err != nil {
			return err
		}
		der, err := x509.MarshalPKCS8PrivateKey(key)
		if err != nil {
			return err
		}
		if err = exclusive(*private, pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: der}), 0600); err != nil {
			return err
		}
		if err = exclusive(*private+".pub", []byte(base64.StdEncoding.EncodeToString(pub)+"\n"), 0644); err != nil {
			return err
		}
		fmt.Println("Public key:", base64.StdEncoding.EncodeToString(pub))
		return nil
	case "package":
		if _, err := update.ParseVersion(*version); err != nil {
			return err
		}
		filename := filepath.Join(*dir, "FolderBridge-v"+*version+"-Windows.zip")
		out, err := os.OpenFile(filename, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0600)
		if err != nil {
			return err
		}
		z := zip.NewWriter(out)
		for _, name := range []string{"FolderBridge.exe", "FolderBridge-Updater.exe"} {
			if err = zipExecutable(z, filepath.Join(*dir, name), name); err != nil {
				z.Close()
				out.Close()
				return err
			}
		}
		if err = z.Close(); err != nil {
			out.Close()
			return err
		}
		if err = out.Sync(); err != nil {
			out.Close()
			return err
		}
		if err = out.Close(); err != nil {
			return err
		}
		hash, err := digest(filename)
		if err != nil {
			return err
		}
		manifest := update.Manifest{Version: *version, Platform: "windows-amd64", URL: "https://github.com/" + update.Repository + "/releases/download/v" + *version + "/" + filepath.Base(filename), SHA256: hash, MinimumUpdaterVersion: update.UpdaterVersion}
		data, err := json.MarshalIndent(manifest, "", "  ")
		if err != nil {
			return err
		}
		return exclusive(filepath.Join(*dir, "manifest.json"), append(data, '\n'), 0644)
	case "sign":
		if *private == "" {
			return errors.New("provide offline --private-key")
		}
		data, err := os.ReadFile(*private)
		if err != nil {
			return err
		}
		block, _ := pem.Decode(data)
		if block == nil {
			return errors.New("invalid private key PEM")
		}
		parsed, err := x509.ParsePKCS8PrivateKey(block.Bytes)
		if err != nil {
			return err
		}
		key, ok := parsed.(ed25519.PrivateKey)
		if !ok {
			return errors.New("key must be Ed25519")
		}
		manifest, err := os.ReadFile(filepath.Join(*dir, "manifest.json"))
		if err != nil {
			return err
		}
		sig := ed25519.Sign(key, manifest)
		m, err := update.VerifyManifest(manifest, sig, key.Public().(ed25519.PublicKey))
		if err != nil {
			return err
		}
		archive := filepath.Join(*dir, filepath.Base(m.URL))
		if err = update.VerifyArchive(archive, m); err != nil {
			return err
		}
		temp, err := os.MkdirTemp("", "folderbridge-offline-review-")
		if err != nil {
			return err
		}
		defer os.RemoveAll(temp)
		if err = update.Extract(archive, temp); err != nil {
			return err
		}
		if err = exclusive(filepath.Join(*dir, "manifest.sig"), sig, 0644); err != nil {
			return err
		}
		fmt.Println("Offline signature (base64):", base64.StdEncoding.EncodeToString(sig))
		return nil
	case "verify":
		encoded := os.Getenv("UPDATE_PUBLIC_KEY")
		if *public != "" {
			b, err := os.ReadFile(*public)
			if err != nil {
				return err
			}
			encoded = strings.TrimSpace(string(b))
		}
		key, err := base64.StdEncoding.DecodeString(encoded)
		if err != nil || len(key) != ed25519.PublicKeySize {
			return errors.New("provide a valid public key")
		}
		manifest, err := os.ReadFile(filepath.Join(*dir, "manifest.json"))
		if err != nil {
			return err
		}
		encodedSig := *signature
		if encodedSig == "" {
			encodedSig = os.Getenv("UPDATE_SIGNATURE")
		}
		if encodedSig != "" {
			sig, err := base64.StdEncoding.DecodeString(encodedSig)
			if err != nil {
				return err
			}
			path := filepath.Join(*dir, "manifest.sig")
			existing, readErr := os.ReadFile(path)
			if readErr == nil {
				if !bytes.Equal(existing, sig) {
					return errors.New("existing release signature differs from supplied offline signature")
				}
			} else if errors.Is(readErr, os.ErrNotExist) {
				if err = exclusive(path, sig, 0644); err != nil {
					return err
				}
			} else {
				return readErr
			}
		}
		sig, err := os.ReadFile(filepath.Join(*dir, "manifest.sig"))
		if err != nil {
			return err
		}
		m, err := update.VerifyManifest(manifest, sig, ed25519.PublicKey(key))
		if err != nil {
			return err
		}
		if *version != "" && m.Version != *version {
			return errors.New("manifest version does not match release tag")
		}
		archive := filepath.Join(*dir, filepath.Base(m.URL))
		if err = update.VerifyArchive(archive, m); err != nil {
			return err
		}
		temp, err := os.MkdirTemp("", "folderbridge-release-verify-")
		if err != nil {
			return err
		}
		defer os.RemoveAll(temp)
		if err = update.Extract(archive, temp); err != nil {
			return err
		}
		for _, name := range []string{"FolderBridge.exe", "FolderBridge-Updater.exe"} {
			a, err := digest(filepath.Join(temp, name))
			if err != nil {
				return err
			}
			b, err := digest(filepath.Join(*dir, name))
			if err != nil {
				return err
			}
			if a != b {
				return fmt.Errorf("standalone %s differs from signed ZIP", name)
			}
		}
		fmt.Println("Verified signed Windows x64 release v" + m.Version)
		return nil
	default:
		return errors.New("unknown release mode")
	}
}
func exclusive(path string, data []byte, mode os.FileMode) error {
	f, err := os.OpenFile(path, os.O_CREATE|os.O_EXCL|os.O_WRONLY, mode)
	if err != nil {
		return err
	}
	_, err = f.Write(data)
	if err == nil {
		err = f.Sync()
	}
	e := f.Close()
	if err != nil {
		return err
	}
	return e
}
func digest(path string) (string, error) {
	f, err := os.Open(path)
	if err != nil {
		return "", err
	}
	defer f.Close()
	h := sha256.New()
	_, err = io.Copy(h, f)
	return hex.EncodeToString(h.Sum(nil)), err
}
func zipExecutable(z *zip.Writer, path, name string) error {
	f, err := os.Open(path)
	if err != nil {
		return err
	}
	defer f.Close()
	header := &zip.FileHeader{Name: name, Method: zip.Deflate}
	header.SetMode(0700)
	out, err := z.CreateHeader(header)
	if err != nil {
		return err
	}
	_, err = io.Copy(out, f)
	return err
}
