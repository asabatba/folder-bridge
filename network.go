package main

import (
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/hex"
	"encoding/json"
	"encoding/pem"
	"errors"
	"fmt"
	"io"
	"math/big"
	"net"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"
)

const peerPort = "48713"

func newTLSCert(certPath, keyPath string) (tls.Certificate, string, error) {
	if _, err := os.Stat(certPath); err == nil {
		cert, err := tls.LoadX509KeyPair(certPath, keyPath)
		if err != nil {
			return tls.Certificate{}, "", err
		}
		return cert, certFingerprint(cert.Certificate[0]), nil
	}
	privateKey, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		return tls.Certificate{}, "", err
	}
	serialMax := new(big.Int).Lsh(big.NewInt(1), 128)
	serial, err := rand.Int(rand.Reader, serialMax)
	if err != nil {
		return tls.Certificate{}, "", err
	}
	now := time.Now()
	tpl := &x509.Certificate{
		SerialNumber: serial, Subject: pkix.Name{CommonName: "FolderBridge Local Peer"},
		NotBefore: now.Add(-time.Hour), NotAfter: now.AddDate(10, 0, 0),
		KeyUsage: x509.KeyUsageDigitalSignature, ExtKeyUsage: []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
		DNSNames: []string{"localhost"}, IPAddresses: []net.IP{net.ParseIP("127.0.0.1")},
	}
	der, err := x509.CreateCertificate(rand.Reader, tpl, tpl, &privateKey.PublicKey, privateKey)
	if err != nil {
		return tls.Certificate{}, "", err
	}
	keyDER, err := x509.MarshalECPrivateKey(privateKey)
	if err != nil {
		return tls.Certificate{}, "", err
	}
	certPEM := pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der})
	keyPEM := pem.EncodeToMemory(&pem.Block{Type: "EC PRIVATE KEY", Bytes: keyDER})
	if err := writeJSONAtomically(certPath, certPEM); err != nil {
		return tls.Certificate{}, "", err
	}
	if err := writeJSONAtomically(keyPath, keyPEM); err != nil {
		return tls.Certificate{}, "", err
	}
	cert, err := tls.X509KeyPair(certPEM, keyPEM)
	if err != nil {
		return tls.Certificate{}, "", err
	}
	return cert, certFingerprint(der), nil
}

func certFingerprint(raw []byte) string {
	sum := sha256.Sum256(raw)
	return hex.EncodeToString(sum[:])
}

func newToken() (string, error) {
	b := make([]byte, 24)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	return hex.EncodeToString(b), nil
}

// A host exposes just this shared directory. It requires both TLS and a random bearer token.
type Host struct {
	root         string
	secret       string
	scanner      *Scanner
	fileMu       sync.RWMutex
	connected    sync.Once
	onEvent      func(string)
	onConnected  func()
	changeMu     sync.Mutex
	changeSeq    uint64
	changeSignal chan struct{}
}

func newHost(root, secret string) *Host {
	return &Host{root: root, secret: secret, scanner: NewScanner(), changeSeq: 1, changeSignal: make(chan struct{})}
}

func (h *Host) handler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /v1/manifest", h.manifest)
	mux.HandleFunc("GET /v2/manifest", h.manifestV2)
	mux.HandleFunc("GET /v3/changes", h.waitForChanges)
	mux.HandleFunc("POST /v3/move", h.moveFile)
	mux.HandleFunc("POST /v2/batch/get", h.batchGet)
	mux.HandleFunc("POST /v2/batch/put", h.batchPut)
	mux.HandleFunc("GET /v1/file", h.readFile)
	mux.HandleFunc("PUT /v1/file", h.putFile)
	mux.HandleFunc("DELETE /v1/file", h.deleteFile)
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		want := "Bearer " + h.secret
		if subtle.ConstantTimeCompare([]byte(r.Header.Get("Authorization")), []byte(want)) != 1 {
			http.Error(w, "unauthorized", http.StatusUnauthorized)
			return
		}
		w.Header().Set("Cache-Control", "no-store")
		h.connected.Do(func() {
			if h.onConnected != nil {
				h.onConnected()
			}
		})
		mux.ServeHTTP(w, r)
	})
}

func (h *Host) manifest(w http.ResponseWriter, r *http.Request) {
	h.fileMu.RLock()
	defer h.fileMu.RUnlock()
	files, err := h.scanner.Scan(h.root)
	if err != nil {
		http.Error(w, err.Error(), http.StatusConflict)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(files)
}

type Manifest struct {
	Files       map[string]FileInfo `json:"files"`
	IgnoreFiles []IgnoreFile        `json:"ignoreFiles"`
	Features    []string            `json:"features,omitempty"`
}

func (h *Host) manifestV2(w http.ResponseWriter, r *http.Request) {
	h.fileMu.RLock()
	defer h.fileMu.RUnlock()
	snapshot, err := h.scanner.ScanManifest(h.root)
	if err != nil {
		http.Error(w, err.Error(), http.StatusConflict)
		return
	}
	snapshot.Features = []string{"atomic-rename"}
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(snapshot)
}

func (h *Host) checkNotIgnored(rel string) error {
	ignored, err := localIgnoreForPath(h.root, rel)
	if err != nil {
		return err
	}
	if ignored {
		return errors.New("path ignored by local rules")
	}
	return nil
}

func (h *Host) safeRequestPath(r *http.Request) (string, error) {
	return checkedPath(h.root, r.URL.Query().Get("path"))
}

func currentHash(filename string) (string, error) {
	info, err := os.Lstat(filename)
	if errors.Is(err, os.ErrNotExist) {
		return "", nil
	}
	if err != nil {
		return "", err
	}
	if !info.Mode().IsRegular() {
		return "", errors.New("not a regular file")
	}
	return hashStableFile(filename, info)
}

func (h *Host) readFile(w http.ResponseWriter, r *http.Request) {
	h.fileMu.RLock()
	defer h.fileMu.RUnlock()
	filename, err := h.safeRequestPath(r)
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	if err := h.checkNotIgnored(r.URL.Query().Get("path")); err != nil {
		http.Error(w, err.Error(), http.StatusConflict)
		return
	}
	got, err := currentHash(filename)
	if err != nil {
		http.Error(w, err.Error(), http.StatusConflict)
		return
	}
	if got == "" {
		http.Error(w, "not found", http.StatusNotFound)
		return
	}
	if want := r.Header.Get("X-Expected-Hash"); got != want {
		http.Error(w, "changed during sync", http.StatusConflict)
		return
	}
	f, err := os.Open(filename)
	if err != nil {
		http.Error(w, err.Error(), http.StatusConflict)
		return
	}
	defer f.Close()
	info, err := f.Stat()
	if err != nil {
		http.Error(w, err.Error(), http.StatusConflict)
		return
	}
	w.Header().Set("Content-Type", "application/octet-stream")
	w.Header().Set("X-File-Hash", got)
	w.Header().Set("X-Mtime", strconv.FormatInt(info.ModTime().UnixNano(), 10))
	w.Header().Set("Content-Length", strconv.FormatInt(info.Size(), 10))
	if _, err := io.Copy(w, f); err == nil && h.onEvent != nil {
		h.onEvent("Sent " + r.URL.Query().Get("path"))
	}
}

func ptrString(s string) *string { return &s }

func checkExpected(filename, expected string) error {
	got, err := currentHash(filename)
	if err != nil {
		return err
	}
	if got != expected {
		return errors.New("destination changed since manifest")
	}
	return nil
}

func (h *Host) putFile(w http.ResponseWriter, r *http.Request) {
	h.fileMu.Lock()
	defer h.fileMu.Unlock()
	filename, err := h.safeRequestPath(r)
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	if err := h.checkNotIgnored(r.URL.Query().Get("path")); err != nil {
		http.Error(w, err.Error(), http.StatusConflict)
		return
	}
	if err := checkExpected(filename, r.Header.Get("X-Expected-Remote")); err != nil {
		http.Error(w, err.Error(), http.StatusConflict)
		return
	}
	hash := r.Header.Get("X-File-Hash")
	if len(hash) != 64 {
		http.Error(w, "invalid hash", http.StatusBadRequest)
		return
	}
	if _, err := hex.DecodeString(hash); err != nil {
		http.Error(w, "invalid hash", http.StatusBadRequest)
		return
	}
	nanos, err := strconv.ParseInt(r.Header.Get("X-Mtime"), 10, 64)
	if err != nil {
		http.Error(w, "invalid mtime", http.StatusBadRequest)
		return
	}
	if err := stagedWriteGuarded(filename, r.Body, hash, time.Unix(0, nanos), ptrString(r.Header.Get("X-Expected-Remote"))); err != nil {
		http.Error(w, err.Error(), http.StatusConflict)
		return
	}
	w.WriteHeader(http.StatusNoContent)
	h.scanner.Invalidate(r.URL.Query().Get("path"), false)
	h.signalChange()
	if h.onEvent != nil {
		h.onEvent("Received " + r.URL.Query().Get("path"))
	}
}

func (h *Host) deleteFile(w http.ResponseWriter, r *http.Request) {
	h.fileMu.Lock()
	defer h.fileMu.Unlock()
	filename, err := h.safeRequestPath(r)
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	if err := h.checkNotIgnored(r.URL.Query().Get("path")); err != nil {
		http.Error(w, err.Error(), http.StatusConflict)
		return
	}
	if err := checkExpected(filename, r.Header.Get("X-Expected-Remote")); err != nil {
		http.Error(w, err.Error(), http.StatusConflict)
		return
	}
	if err := os.Remove(filename); err != nil && !errors.Is(err, os.ErrNotExist) {
		http.Error(w, err.Error(), http.StatusConflict)
		return
	}
	w.WriteHeader(http.StatusNoContent)
	h.scanner.Invalidate(r.URL.Query().Get("path"), false)
	h.signalChange()
	if h.onEvent != nil {
		h.onEvent("Deleted " + r.URL.Query().Get("path"))
	}
}

// A move is conditional on the old file being unchanged and the new path being
// absent. Both paths must be allowed by this machine's ignore policy.
func (h *Host) moveFile(w http.ResponseWriter, r *http.Request) {
	h.fileMu.Lock()
	defer h.fileMu.Unlock()
	var req struct {
		From string `json:"from"`
		To   string `json:"to"`
		Hash string `json:"hash"`
	}
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 4096)).Decode(&req); err != nil {
		http.Error(w, "invalid move request: "+err.Error(), http.StatusBadRequest)
		return
	}
	if len(req.Hash) != 64 {
		http.Error(w, "invalid hash", http.StatusBadRequest)
		return
	}
	if _, err := hex.DecodeString(req.Hash); err != nil {
		http.Error(w, "invalid hash", http.StatusBadRequest)
		return
	}
	for _, rel := range []string{req.From, req.To} {
		if _, err := checkedPath(h.root, rel); err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		if err := h.checkNotIgnored(rel); err != nil {
			http.Error(w, err.Error(), http.StatusConflict)
			return
		}
	}
	if err := moveUnchanged(h.root, req.From, req.To, req.Hash); err != nil {
		http.Error(w, "move refused: "+err.Error(), http.StatusConflict)
		return
	}
	h.scanner.Invalidate(req.From, false)
	h.scanner.Invalidate(req.To, false)
	h.signalChange()
	if h.onEvent != nil {
		h.onEvent("Moved " + req.From + " → " + req.To)
	}
	w.WriteHeader(http.StatusNoContent)
}

// Invite contains the host's reachable LAN address, certificate pin and a secret.
// Treat it as a password: only share it with the other device.
type Invite struct{ Addr, Token, Fingerprint string }

func parseInvite(raw string) (Invite, error) {
	u, err := url.Parse(strings.TrimSpace(raw))
	if err != nil {
		return Invite{}, err
	}
	if u.Scheme != "folderbridge" || u.User != nil || u.Host == "" || u.Path == "" {
		return Invite{}, errors.New("expected folderbridge://HOST:48713/TOKEN?fp=SHA256")
	}
	if _, _, err := net.SplitHostPort(u.Host); err != nil {
		return Invite{}, errors.New("pairing link requires an IP/hostname and port")
	}
	token := strings.TrimPrefix(u.EscapedPath(), "/")
	fingerprint := u.Query().Get("fp")
	if len(token) != 48 || len(fingerprint) != 64 {
		return Invite{}, errors.New("invalid pairing link")
	}
	if _, err := hex.DecodeString(token); err != nil {
		return Invite{}, errors.New("invalid pairing secret")
	}
	if _, err := hex.DecodeString(fingerprint); err != nil {
		return Invite{}, errors.New("invalid certificate pin")
	}
	return Invite{Addr: u.Host, Token: token, Fingerprint: strings.ToLower(fingerprint)}, nil
}

func formatInvite(address, token, fingerprint string) string {
	return "folderbridge://" + net.JoinHostPort(address, peerPort) + "/" + token + "?fp=" + fingerprint
}

// HTTPStatusError preserves the status code so retries never replay conflicts.
type HTTPStatusError struct {
	Status int
	Detail string
}

func (e *HTTPStatusError) Error() string {
	return fmt.Sprintf("remote status %d: %s", e.Status, e.Detail)
}

type Peer struct {
	baseURL string
	token   string
	client  *http.Client
	tuner   transferTuner
}

func newPeer(invite Invite) *Peer {
	pin, _ := hex.DecodeString(invite.Fingerprint)
	transport := &http.Transport{
		Proxy: nil,
		TLSClientConfig: &tls.Config{
			MinVersion: tls.VersionTLS13,
			// A self-signed certificate is verified using its exact SHA-256 fingerprint.
			InsecureSkipVerify: true,
			VerifyConnection: func(c tls.ConnectionState) error {
				if len(c.PeerCertificates) != 1 {
					return errors.New("unexpected TLS certificate chain")
				}
				actual := sha256.Sum256(c.PeerCertificates[0].Raw)
				if subtle.ConstantTimeCompare(actual[:], pin) != 1 {
					return errors.New("certificate fingerprint does not match pairing link")
				}
				return nil
			},
		},
	}
	return &Peer{
		baseURL: "https://" + invite.Addr,
		token:   invite.Token,
		client:  &http.Client{Transport: transport, Timeout: 30 * time.Minute},
	}
}

func (p *Peer) request(ctx context.Context, method, endpoint string, body io.Reader, headers map[string]string) (*http.Response, error) {
	req, err := http.NewRequestWithContext(ctx, method, p.baseURL+endpoint, body)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Authorization", "Bearer "+p.token)
	for k, v := range headers {
		req.Header.Set(k, v)
	}
	resp, err := p.client.Do(req)
	if err != nil {
		return nil, err
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		data, _ := io.ReadAll(io.LimitReader(resp.Body, 1024))
		resp.Body.Close()
		return nil, &HTTPStatusError{Status: resp.StatusCode, Detail: strings.TrimSpace(string(data))}
	}
	return resp, nil
}

func (p *Peer) manifest(ctx context.Context) (Manifest, error) {
	resp, err := p.request(ctx, "GET", "/v2/manifest", nil, nil)
	if err != nil {
		return Manifest{}, fmt.Errorf("v2 manifest (update FolderBridge on both PCs): %w", err)
	}
	defer resp.Body.Close()
	var manifest Manifest
	if err := json.NewDecoder(io.LimitReader(resp.Body, 128<<20)).Decode(&manifest); err != nil {
		return Manifest{}, err
	}
	if manifest.Files == nil {
		return Manifest{}, errors.New("empty manifest")
	}
	if _, err := newIgnoreMatcher(manifest.IgnoreFiles); err != nil {
		return Manifest{}, err
	}
	return manifest, nil
}

func (p *Peer) get(ctx context.Context, root, rel string, entry FileInfo, originalHash string) error {
	return p.getAs(ctx, root, rel, rel, entry, originalHash)
}

func (p *Peer) getAs(ctx context.Context, root, remoteRel, localRel string, entry FileInfo, originalHash string) error {
	dest, err := checkedPath(root, localRel)
	if err != nil {
		return err
	}
	endpoint := "/v1/file?path=" + url.QueryEscape(remoteRel)
	resp, err := p.request(ctx, "GET", endpoint, nil, map[string]string{"X-Expected-Hash": entry.Hash})
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	return stagedWriteGuarded(dest, resp.Body, entry.Hash, time.Unix(0, entry.MTime), &originalHash)
}

func (p *Peer) put(ctx context.Context, root, rel string, local FileInfo, remoteHash string) error {
	src, err := checkedPath(root, rel)
	if err != nil {
		return err
	}
	f, err := os.Open(src)
	if err != nil {
		return err
	}
	defer f.Close()
	endpoint := "/v1/file?path=" + url.QueryEscape(rel)
	resp, err := p.request(ctx, "PUT", endpoint, f, map[string]string{
		"X-Expected-Remote": remoteHash,
		"X-File-Hash":       local.Hash,
		"X-Mtime":           strconv.FormatInt(local.MTime, 10),
	})
	if err != nil {
		return err
	}
	resp.Body.Close()
	return nil
}

func (p *Peer) delete(ctx context.Context, rel, remoteHash string) error {
	resp, err := p.request(ctx, "DELETE", "/v1/file?path="+url.QueryEscape(rel), nil,
		map[string]string{"X-Expected-Remote": remoteHash})
	if err != nil {
		return err
	}
	resp.Body.Close()
	return nil
}

func (p *Peer) move(ctx context.Context, from, to, hash string) error {
	data, err := json.Marshal(struct {
		From string `json:"from"`
		To   string `json:"to"`
		Hash string `json:"hash"`
	}{from, to, hash})
	if err != nil {
		return err
	}
	resp, err := p.request(ctx, "POST", "/v3/move", strings.NewReader(string(data)),
		map[string]string{"Content-Type": "application/json"})
	if err != nil {
		return err
	}
	return resp.Body.Close()
}

func (p *Peer) Close() { p.client.CloseIdleConnections() }

func lanAddresses() []string {
	interfaces, err := net.Interfaces()
	if err != nil {
		return nil
	}
	var out []string
	seen := make(map[string]bool)
	for _, iface := range interfaces {
		if iface.Flags&net.FlagUp == 0 || iface.Flags&net.FlagLoopback != 0 {
			continue
		}
		addrs, err := iface.Addrs()
		if err != nil {
			continue
		}
		for _, addr := range addrs {
			ip, _, err := net.ParseCIDR(addr.String())
			if err != nil || ip.To4() == nil || ip.IsLinkLocalUnicast() {
				continue
			}
			if !seen[ip.String()] {
				out = append(out, ip.String())
				seen[ip.String()] = true
			}
		}
	}
	return out
}

func configHome() (string, error) {
	base, err := os.UserConfigDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(base, "FolderBridge"), nil
}
