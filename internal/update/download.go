package update

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"time"
)

// GitHub release assets redirect to a separate HTTPS CDN. Never allow arbitrary
// redirect hosts, credentials, ports or HTTP downgrades (including via proxies).
func TrustedRedirect(u *url.URL) bool {
	if u.Scheme != "https" || u.User != nil {
		return false
	}
	switch u.Host {
	case "github.com", "api.github.com", "release-assets.githubusercontent.com", "objects.githubusercontent.com":
		return true
	}
	return false
}
func HTTPClient() *http.Client {
	return &http.Client{Timeout: 10 * time.Minute, CheckRedirect: func(req *http.Request, via []*http.Request) error {
		if len(via) >= 5 || !TrustedRedirect(req.URL) {
			return errors.New("untrusted update redirect")
		}
		return nil
	}}
}
func fetch(ctx context.Context, c *http.Client, raw string, out io.Writer, max int64) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, raw, nil)
	if err != nil {
		return err
	}
	if !TrustedRedirect(req.URL) {
		return errors.New("untrusted update URL")
	}
	req.Header.Set("User-Agent", "FolderBridge/"+Version)
	req.Header.Set("Accept", "application/octet-stream")
	res, err := c.Do(req)
	if err != nil {
		return err
	}
	defer res.Body.Close()
	if res.StatusCode != http.StatusOK {
		return fmt.Errorf("release server returned HTTP %d", res.StatusCode)
	}
	if res.ContentLength > max {
		return errors.New("release response too large")
	}
	n, err := io.Copy(out, io.LimitReader(res.Body, max+1))
	if err != nil {
		return err
	}
	if n > max {
		return errors.New("release response too large")
	}
	return nil
}
func fetchBytes(ctx context.Context, c *http.Client, raw string, max int64) ([]byte, error) {
	var b limitedBuffer
	err := fetch(ctx, c, raw, &b, max)
	return b.data, err
}

type limitedBuffer struct{ data []byte }

func (b *limitedBuffer) Write(p []byte) (int, error) {
	b.data = append(b.data, p...)
	return len(p), nil
}

// Discover treats GitHub metadata only as a locator. The signed manifest is the
// authority; metadata cannot override its version or artifact location.
func Discover(ctx context.Context, c *http.Client) (Manifest, []byte, []byte, error) {
	var zero Manifest
	key, err := Key()
	if err != nil {
		return zero, nil, nil, err
	}
	data, err := fetchBytes(ctx, c, "https://api.github.com/repos/"+Repository+"/releases/latest", 1<<20)
	if err != nil {
		return zero, nil, nil, err
	}
	var release struct {
		Tag        string `json:"tag_name"`
		Draft      bool   `json:"draft"`
		Prerelease bool   `json:"prerelease"`
	}
	if err = json.Unmarshal(data, &release); err != nil {
		return zero, nil, nil, err
	}
	if release.Draft || release.Prerelease || len(release.Tag) < 2 || release.Tag[0] != 'v' {
		return zero, nil, nil, errors.New("not a stable release")
	}
	version := release.Tag[1:]
	if _, err = ParseVersion(version); err != nil {
		return zero, nil, nil, err
	}
	base := "https://github.com/" + Repository + "/releases/download/" + release.Tag + "/"
	manifest, err := fetchBytes(ctx, c, base+"manifest.json", 32<<10)
	if err != nil {
		return zero, nil, nil, err
	}
	sig, err := fetchBytes(ctx, c, base+"manifest.sig", 64)
	if err != nil {
		return zero, nil, nil, err
	}
	m, err := VerifyManifest(manifest, sig, key)
	if err == nil && m.Version != version {
		err = errors.New("release tag and signed version differ")
	}
	return m, manifest, sig, err
}

// Download retries from byte zero into an unpublished staging file. Memory is
// bounded independently of artifact size; a failed attempt never affects app.
func Download(ctx context.Context, c *http.Client, dir string, m Manifest) error {
	filename := filepath.Join(dir, "release.zip")
	for attempt := 0; attempt < 3; attempt++ {
		err := downloadOnce(ctx, c, filename, m)
		if err == nil {
			return nil
		}
		os.Remove(filename)
		if attempt == 2 {
			return err
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(time.Duration(1<<attempt) * time.Second):
		}
	}
	return errors.New("download failed")
}
func downloadOnce(ctx context.Context, c *http.Client, filename string, m Manifest) error {
	f, err := os.OpenFile(filename, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0600)
	if err != nil {
		return err
	}
	err = fetch(ctx, c, m.URL, f, MaxArchive)
	if err == nil {
		err = f.Sync()
	}
	closeErr := f.Close()
	if err != nil {
		return err
	}
	if closeErr != nil {
		return closeErr
	}
	return VerifyArchive(filename, m)
}
