package update

import (
	"archive/zip"
	"bytes"
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/binary"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

func testKey(t *testing.T) (ed25519.PublicKey, ed25519.PrivateKey) {
	t.Helper()
	pub, key, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	old := PublicKey
	PublicKey = base64.StdEncoding.EncodeToString(pub)
	t.Cleanup(func() { PublicKey = old })
	return pub, key
}
func testManifest(version string, payload []byte) Manifest {
	sum := sha256.Sum256(payload)
	return Manifest{Version: version, Platform: "windows-amd64", URL: "https://github.com/" + Repository + "/releases/download/v" + version + "/FolderBridge-v" + version + "-Windows.zip", SHA256: hex.EncodeToString(sum[:]), MinimumUpdaterVersion: "1.0.0"}
}
func testPE() []byte {
	b := make([]byte, 128)
	copy(b, "MZ")
	binary.LittleEndian.PutUint32(b[60:], 64)
	copy(b[64:], "PE\x00\x00")
	binary.LittleEndian.PutUint16(b[68:], 0x8664)
	binary.LittleEndian.PutUint16(b[86:], 2)
	binary.LittleEndian.PutUint16(b[88:], 0x20b)
	return b
}
func testZIP(t *testing.T, entries []string, payload []byte) []byte {
	t.Helper()
	var b bytes.Buffer
	z := zip.NewWriter(&b)
	for _, name := range entries {
		h := &zip.FileHeader{Name: name, Method: zip.Deflate}
		h.SetMode(0700)
		w, err := z.CreateHeader(h)
		if err != nil {
			t.Fatal(err)
		}
		if _, err = w.Write(payload); err != nil {
			t.Fatal(err)
		}
	}
	if err := z.Close(); err != nil {
		t.Fatal(err)
	}
	return b.Bytes()
}
func TestManifestTrust(t *testing.T) {
	pub, key := testKey(t)
	m := testManifest("3.4.0", nil)
	data, _ := json.Marshal(m)
	sig := ed25519.Sign(key, data)
	if _, err := VerifyManifest(data, sig, pub); err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		name   string
		modify func(*Manifest)
	}{
		{"platform", func(m *Manifest) { m.Platform = "linux-amd64" }},
		{"new updater", func(m *Manifest) { m.MinimumUpdaterVersion = "2.0.0" }},
		{"bad version", func(m *Manifest) { m.Version = "3.4.0-rc.1" }},
		{"bad hash", func(m *Manifest) { m.SHA256 = "bad" }},
		{"HTTP", func(m *Manifest) { m.URL = strings.Replace(m.URL, "https:", "http:", 1) }},
		{"other repo", func(m *Manifest) { m.URL = strings.Replace(m.URL, Repository, "attacker/repo", 1) }},
		{"userinfo", func(m *Manifest) { m.URL = strings.Replace(m.URL, "github.com", "github.com@evil.example", 1) }},
		{"query", func(m *Manifest) { m.URL += "?redirect=evil" }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			bad := m
			tc.modify(&bad)
			b, _ := json.Marshal(bad)
			if _, err := VerifyManifest(b, ed25519.Sign(key, b), pub); err == nil {
				t.Fatal("accepted incompatible/untrusted manifest")
			}
		})
	}
	for _, bad := range [][]byte{nil, []byte("unsigned"), append([]byte(nil), sig...)} {
		if len(bad) == 64 {
			bad[0] ^= 1
		}
		if _, err := VerifyManifest(data, bad, pub); err == nil {
			t.Fatal("accepted invalid signature")
		}
	}
	if _, err := VerifyManifest(append(data, '\n'), sig, pub); err == nil {
		t.Fatal("signature did not cover exact bytes")
	}
	PublicKey = ""
	if _, err := Key(); err == nil {
		t.Fatal("missing public key did not fail closed")
	}
}
func TestStableVersionComparison(t *testing.T) {
	for _, tc := range []struct {
		a, b string
		want int
	}{{"3.3.0", "3.3.0", 0}, {"3.10.0", "3.9.9", 1}, {"3.2.0", "3.3.0", -1}, {"4.0.0", "3.99.99", 1}} {
		got, err := Compare(tc.a, tc.b)
		if err != nil || got != tc.want {
			t.Fatalf("Compare %+v = %d %v", tc, got, err)
		}
	}
	for _, bad := range []string{"v3.3.0", "3.3", "03.3.0", "3.3.0-rc.1", "3.3.0+build", "3.-1.0", "3.3.0.0", "3.3.18446744073709551616"} {
		if _, err := ParseVersion(bad); err == nil {
			t.Fatal("accepted", bad)
		}
	}
}
func TestArchiveSafety(t *testing.T) {
	good := testZIP(t, []string{"FolderBridge.exe", "FolderBridge-Updater.exe"}, testPE())
	dir := t.TempDir()
	path := filepath.Join(dir, "release.zip")
	os.WriteFile(path, good, 0600)
	if err := VerifyArchive(path, testManifest("3.4.0", good)); err != nil {
		t.Fatal(err)
	}
	os.WriteFile(path, append(good, 1), 0600)
	if err := VerifyArchive(path, testManifest("3.4.0", good)); err == nil {
		t.Fatal("modified ZIP accepted")
	}
	for _, entries := range [][]string{
		{"FolderBridge.exe", "FolderBridge-Updater.exe"},
		{"../FolderBridge.exe", "FolderBridge-Updater.exe"},
		{"/FolderBridge.exe", "FolderBridge-Updater.exe"},
		{"C:\\FolderBridge.exe", "FolderBridge-Updater.exe"},
		{"FolderBridge.exe:evil", "FolderBridge-Updater.exe"},
		{"FolderBridge.exe", "FolderBridge.exe"},
		{"folderbridge.exe", "FolderBridge-Updater.exe"},
		{"FolderBridge.exe"},
		{"FolderBridge.exe", "FolderBridge-Updater.exe", "extra.txt"},
	} {
		data := testZIP(t, entries, testPE())
		os.WriteFile(path, data, 0600)
		err := Extract(path, t.TempDir())
		valid := len(entries) == 2 && entries[0] == "FolderBridge.exe" && entries[1] == "FolderBridge-Updater.exe"
		if valid && err != nil {
			t.Fatal(err)
		}
		if !valid && err == nil {
			t.Fatal("unsafe archive accepted", entries)
		}
	}
	// Windows symlink mode must be rejected even with an expected filename.
	var b bytes.Buffer
	z := zip.NewWriter(&b)
	h := &zip.FileHeader{Name: "FolderBridge.exe"}
	h.SetMode(os.ModeSymlink | 0777)
	w, _ := z.CreateHeader(h)
	w.Write([]byte("other.exe"))
	w, _ = z.Create("FolderBridge-Updater.exe")
	w.Write(testPE())
	z.Close()
	os.WriteFile(path, b.Bytes(), 0600)
	if err := Extract(path, t.TempDir()); err == nil {
		t.Fatal("symlink accepted")
	}
	data := testZIP(t, []string{"FolderBridge.exe", "FolderBridge-Updater.exe"}, []byte("not a PE"))
	os.WriteFile(path, data, 0600)
	if err := Extract(path, t.TempDir()); err == nil {
		t.Fatal("non-executable accepted")
	}
	// Falsify the central-directory extraction size without allocating a ZIP bomb.
	data = append([]byte(nil), good...)
	pos := bytes.Index(data, []byte{'P', 'K', 1, 2})
	if pos < 0 {
		t.Fatal("missing ZIP directory")
	}
	binary.LittleEndian.PutUint32(data[pos+24:], MaxExtracted+1)
	os.WriteFile(path, data, 0600)
	if err := Extract(path, t.TempDir()); err == nil {
		t.Fatal("excessive extraction accepted")
	}
}
func TestTrustedDownloadRedirects(t *testing.T) {
	for _, raw := range []string{"https://release-assets.githubusercontent.com/asset?token=x", "https://objects.githubusercontent.com/asset"} {
		u, _ := url.Parse(raw)
		if !TrustedRedirect(u) {
			t.Fatal("rejected CDN", raw)
		}
	}
	for _, raw := range []string{"http://github.com/a", "https://github.com.evil/a", "https://github.com:444/a", "https://user@github.com/a", "https://127.0.0.1/a", "https://evil.example/a"} {
		u, _ := url.Parse(raw)
		if TrustedRedirect(u) {
			t.Fatal("accepted redirect", raw)
		}
		if HTTPClient().CheckRedirect(&http.Request{URL: u}, nil) == nil {
			t.Fatal("redirect policy bypass")
		}
	}
}

type roundTripFunc func(*http.Request) (*http.Response, error)

func (f roundTripFunc) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }
func response(data []byte) *http.Response {
	return &http.Response{StatusCode: 200, Body: io.NopCloser(bytes.NewReader(data)), ContentLength: int64(len(data)), Header: make(http.Header)}
}

type brokenBody struct{}

func (brokenBody) Read([]byte) (int, error) { return 0, io.ErrUnexpectedEOF }
func (brokenBody) Close() error             { return nil }
func TestDownloadInterruptionRetry(t *testing.T) {
	var calls atomic.Int32
	data := testZIP(t, []string{"FolderBridge.exe", "FolderBridge-Updater.exe"}, testPE())
	m := testManifest("3.4.0", data)
	c := HTTPClient()
	c.Transport = roundTripFunc(func(*http.Request) (*http.Response, error) {
		if calls.Add(1) == 1 {
			return &http.Response{StatusCode: 200, Body: brokenBody{}}, nil
		}
		return response(data), nil
	})
	dir := t.TempDir()
	if err := Download(context.Background(), c, dir, m); err != nil {
		t.Fatal(err)
	}
	if calls.Load() != 2 {
		t.Fatal("did not retry interrupted stream")
	}
	if err := VerifyArchive(filepath.Join(dir, "release.zip"), m); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	c.Transport = roundTripFunc(func(r *http.Request) (*http.Response, error) { return nil, r.Context().Err() })
	if err := Download(ctx, c, t.TempDir(), m); !errors.Is(err, context.Canceled) {
		t.Fatalf("cancellation ignored: %v", err)
	}
}
func TestResponseBound(t *testing.T) {
	c := HTTPClient()
	c.Transport = roundTripFunc(func(*http.Request) (*http.Response, error) {
		r := response(make([]byte, 100))
		r.ContentLength = -1
		return r, nil
	})
	if _, err := fetchBytes(context.Background(), c, "https://github.com/bounded", 64); err == nil {
		t.Fatal("unbounded response accepted")
	}
}
func TestHealthURLPolicy(t *testing.T) {
	for _, raw := range []string{"http://localhost:1234/healthy", "https://127.0.0.1:1234/healthy", "http://127.0.0.1:0/healthy", "http://127.0.0.1:1234/other", "http://evil.example:1234/healthy", "http://127.0.0.1:1234/healthy?x=y"} {
		if _, err := urlForHealth(raw); err == nil {
			t.Fatal("accepted health URL", raw)
		}
	}
	if _, err := urlForHealth("http://127.0.0.1:1234/healthy"); err != nil {
		t.Fatal(err)
	}
}
func TestExplicitRetrySurvivesRestartButNewFailureQuarantinesAgain(t *testing.T) {
	_, key := testKey(t)
	home := t.TempDir()
	resultPath := filepath.Join(home, "update-result.json")
	if err := WriteJSON(resultPath, map[string]string{"id": "attempt-one", "version": "3.4.0", "failedVersion": "3.4.0", "message": "Rolled back"}); err != nil {
		t.Fatal(err)
	}
	m := NewManager(home, nil)
	m.status.Settings = DefaultSettings()
	m.status.Settings.AutoDownload = false
	manifest := testManifest("3.4.0", nil)
	data, _ := json.Marshal(manifest)
	m.client.Transport = roundTripFunc(func(r *http.Request) (*http.Response, error) {
		switch {
		case strings.HasSuffix(r.URL.Path, "/latest"):
			return response([]byte(`{"tag_name":"v3.4.0"}`)), nil
		case strings.HasSuffix(r.URL.Path, "manifest.json"):
			return response(data), nil
		case strings.HasSuffix(r.URL.Path, "manifest.sig"):
			return response(ed25519.Sign(key, data)), nil
		}
		return nil, errors.New("unexpected download")
	})
	if err := m.Check(context.Background(), false, true); err != nil {
		t.Fatal(err)
	}
	if NewManager(home, nil).Status().FailedVersion != "" {
		t.Fatal("explicit retry was forgotten on restart")
	}
	if err := WriteJSON(resultPath, map[string]string{"id": "attempt-two", "version": "3.4.0", "failedVersion": "3.4.0", "message": "Rolled back"}); err != nil {
		t.Fatal(err)
	}
	if NewManager(home, nil).Status().FailedVersion != "3.4.0" {
		t.Fatal("a new failed attempt escaped quarantine")
	}
}

func TestInstallationResultRefreshesAfterAppStartup(t *testing.T) {
	home := t.TempDir()
	m := NewManager(home, nil)
	result := Result{ID: "new-confirmed-attempt", Version: "3.4.0", Message: "Installed v3.4.0; initialization healthy"}
	if err := WriteJSON(filepath.Join(home, "update-result.json"), result); err != nil {
		t.Fatal(err)
	}
	if got := m.Status().InstallationResult; got != result.Message {
		t.Fatalf("late helper result not displayed: %q", got)
	}
}

func TestSettingsNeverAutoInstall(t *testing.T) {
	s := DefaultSettings()
	if err := s.Validate(); err != nil {
		t.Fatal(err)
	}
	s.AutoInstall = true
	if s.Validate() == nil {
		t.Fatal("autoInstall accepted")
	}
	s = DefaultSettings()
	s.Channel = "beta"
	if s.Validate() == nil {
		t.Fatal("beta accepted")
	}
}
func TestManagerSameSkippedFailedAndNewer(t *testing.T) {
	_, key := testKey(t)
	payload := testZIP(t, []string{"FolderBridge.exe", "FolderBridge-Updater.exe"}, testPE())
	var releaseVersion string
	var downloads atomic.Int32
	notifications := 0
	m := NewManager(t.TempDir(), func(string) { notifications++ })
	m.status.Settings = DefaultSettings()
	m.client = HTTPClient()
	m.client.Transport = roundTripFunc(func(r *http.Request) (*http.Response, error) {
		manifest := testManifest(releaseVersion, payload)
		data, _ := json.Marshal(manifest)
		switch {
		case strings.HasSuffix(r.URL.Path, "/latest"):
			return response([]byte(`{"tag_name":"v` + releaseVersion + `"}`)), nil
		case strings.HasSuffix(r.URL.Path, "manifest.json"):
			return response(data), nil
		case strings.HasSuffix(r.URL.Path, "manifest.sig"):
			return response(ed25519.Sign(key, data)), nil
		case strings.HasSuffix(r.URL.Path, ".zip"):
			downloads.Add(1)
			return response(payload), nil
		}
		return nil, errors.New("unexpected request")
	})
	releaseVersion = Version
	if err := m.Check(context.Background(), false, false); err != nil {
		t.Fatal(err)
	}
	if downloads.Load() != 0 || m.Status().Ready {
		t.Fatal("same version downloaded")
	}
	releaseVersion = "3.4.0"
	m.status.Settings.SkippedVersion = releaseVersion
	if err := m.Check(context.Background(), false, false); err != nil {
		t.Fatal(err)
	}
	if downloads.Load() != 0 || notifications != 0 {
		t.Fatal("skipped version prompted")
	}
	m.status.Settings.SkippedVersion = ""
	m.status.FailedVersion = releaseVersion
	if err := m.Check(context.Background(), false, false); err != nil {
		t.Fatal(err)
	}
	if downloads.Load() != 0 {
		t.Fatal("quarantined version downloaded")
	}
	if err := m.Check(context.Background(), true, true); err != nil {
		t.Fatal(err)
	}
	if !m.Status().Ready || downloads.Load() != 1 || notifications != 1 {
		t.Fatal("explicit retry failed", m.Status())
	}
	if err := m.Check(context.Background(), false, false); err != nil {
		t.Fatal(err)
	}
	if m.Status().FailedVersion != "" || downloads.Load() != 1 {
		t.Fatal("retry approval was not retained for this attempt")
	}
	releaseVersion = "3.5.0"
	if err := m.Check(context.Background(), false, false); err != nil {
		t.Fatal(err)
	}
	if downloads.Load() != 2 || m.Status().AvailableVersion != "3.5.0" {
		t.Fatal("newer version reused stale ZIP")
	}
	restored := NewManager(m.home, nil)
	if !restored.Status().Ready || restored.Status().AvailableVersion != "3.5.0" {
		t.Fatal("verified download not restored across restart")
	}
	if err := os.WriteFile(filepath.Join(m.stage, "release.zip"), []byte("tampered after restart"), 0600); err != nil {
		t.Fatal(err)
	}
	if NewManager(m.home, nil).Status().Ready {
		t.Fatal("tampered cached release restored as ready")
	}
	m.status.Settings.Enabled = false
	if err := m.Check(context.Background(), true, true); err == nil {
		t.Fatal("disabled update check accepted")
	}
	if _, err := m.BeginInstall(); err == nil {
		t.Fatal("disabled update installed")
	}
}
func TestManagerDisablingCancelsDownload(t *testing.T) {
	_, key := testKey(t)
	m := NewManager(t.TempDir(), nil)
	m.status.Settings = DefaultSettings()
	payload := testZIP(t, []string{"FolderBridge.exe", "FolderBridge-Updater.exe"}, testPE())
	manifest := testManifest("3.4.0", payload)
	data, _ := json.Marshal(manifest)
	downloading := make(chan struct{})
	m.client.Transport = roundTripFunc(func(r *http.Request) (*http.Response, error) {
		switch {
		case strings.HasSuffix(r.URL.Path, "/latest"):
			return response([]byte(`{"tag_name":"v3.4.0"}`)), nil
		case strings.HasSuffix(r.URL.Path, "manifest.json"):
			return response(data), nil
		case strings.HasSuffix(r.URL.Path, "manifest.sig"):
			return response(ed25519.Sign(key, data)), nil
		default:
			close(downloading)
			<-r.Context().Done()
			return nil, r.Context().Err()
		}
	})
	done := make(chan error, 1)
	go func() { done <- m.Check(context.Background(), false, false) }()
	select {
	case <-downloading:
	case <-time.After(5 * time.Second):
		t.Fatal("download never started")
	}
	s := DefaultSettings()
	s.Enabled = false
	m.Start(s)
	if err := <-done; !errors.Is(err, context.Canceled) {
		t.Fatalf("download not canceled: %v", err)
	}
	if m.Status().State != "disabled" || m.Status().Ready {
		t.Fatal("disabled downloader became ready")
	}
}
