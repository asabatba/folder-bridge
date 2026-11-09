package syncengine

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"crypto/tls"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"

	"folderbridge/internal/fileops"
)

// SessionCredentials is persisted by the application, separately from the
// engine's durable causal state. An invite is a password: all members are trusted.
type SessionCredentials struct{ ID, Secret string }

type sessionInvite struct {
	SessionCredentials
	Address, Fingerprint string
}

func randomHex(n int) (string, error) {
	b := make([]byte, n)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	return hex.EncodeToString(b), nil
}

func validLANAddress(address string) bool {
	host, port, err := net.SplitHostPort(address)
	if err != nil {
		return false
	}
	n, err := strconv.Atoi(port)
	if err != nil || n < 1 || n > 65535 {
		return false
	}
	if i := strings.LastIndex(host, "%"); i >= 0 {
		host = host[:i]
	}
	ip := net.ParseIP(host)
	return ip != nil && !ip.IsUnspecified() && !ip.IsMulticast() && (ip.IsPrivate() || ip.IsLoopback() || ip.IsLinkLocalUnicast())
}

func validateMember(m sessionMember) error {
	if !validHex(m.ID, 32) || len(m.Name) > 128 || len(m.Addresses) > 32 {
		return errors.New("invalid session member")
	}
	for _, addr := range m.Addresses {
		if !validLANAddress(addr) {
			return errors.New("member address must be a LAN IP and port")
		}
	}
	return nil
}

func parseSessionInvite(raw string) (sessionInvite, error) {
	u, err := url.Parse(strings.TrimSpace(raw))
	if err != nil {
		return sessionInvite{}, err
	}
	if u.Scheme != "folderbridge" || u.User != nil || u.Fragment != "" || u.Query().Get("v") != "4" || !validLANAddress(u.Host) {
		return sessionInvite{}, errors.New("use a new LAN session invitation (v4); old host/guest links require fresh pairing")
	}
	i := sessionInvite{SessionCredentials: SessionCredentials{ID: u.Query().Get("session"), Secret: strings.TrimPrefix(u.Path, "/")}, Address: u.Host, Fingerprint: u.Query().Get("fp")}
	if !validHex(i.ID, 16) || !validHex(i.Secret, 24) || !validHex(i.Fingerprint, 32) {
		return sessionInvite{}, errors.New("invalid session invitation")
	}
	return i, nil
}

func formatSessionInvite(c SessionCredentials, address, pin string) string {
	return "folderbridge://" + address + "/" + c.Secret + "?v=4&session=" + c.ID + "&fp=" + pin
}

// PrepareSession creates an independent, durable session state before settings
// are switched. Old state is never interpreted as a new session's deletion log.
func (a *Engine) PrepareSession(folder, invite string) (SessionCredentials, error) {
	a.controlMu.Lock()
	defer a.controlMu.Unlock()
	a.mu.RLock()
	active := a.session != nil
	a.mu.RUnlock()
	if active {
		return SessionCredentials{}, errors.New("stop the active session before preparing another")
	}
	if err := fileops.CheckSyncRoot(folder); err != nil {
		return SessionCredentials{}, err
	}
	err := filepath.WalkDir(folder, func(p string, d os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.Type()&os.ModeSymlink != 0 {
			return nil
		}
		if d.IsDir() && d.Name() == ".git" {
			return filepath.SkipDir
		}
		if !d.IsDir() && d.Name() == ".folderbridgeignore" {
			return errors.New("rename .folderbridgeignore to .fbignore before starting the new session (the old name is no longer an ignore file)")
		}
		return nil
	})
	if err != nil {
		return SessionCredentials{}, err
	}
	_, pin, err := newTLSCert(filepath.Join(a.home, "peer-cert.pem"), filepath.Join(a.home, "peer-key.pem"))
	if err != nil {
		return SessionCredentials{}, err
	}
	var c SessionCredentials
	members := map[string]sessionMember{}
	if invite != "" {
		i, err := parseSessionInvite(invite)
		if err != nil {
			return c, err
		}
		c = i.SessionCredentials
		if i.Fingerprint == pin {
			return c, errors.New("this invitation belongs to this computer; join from another computer")
		}
		members[i.Fingerprint] = sessionMember{ID: i.Fingerprint, Name: "Inviting computer", Addresses: []string{i.Address}}
	} else {
		c.ID, err = randomHex(16)
		if err != nil {
			return c, err
		}
		c.Secret, err = randomHex(24)
		if err != nil {
			return c, err
		}
	}
	filename := filepath.Join(a.home, "session-"+c.ID+".json")
	if _, err := os.Stat(filename); err == nil {
		// Rejoining this same v4 session resumes its own causal history. It is
		// never imported into another session or reset to an empty baseline.
		s, err := loadSessionState(filename, c.ID, pin, folder)
		if err != nil {
			return c, err
		}
		for id, seed := range members {
			member := s.Members[id]
			member.ID = id
			if member.Name == "" {
				member.Name = seed.Name
			}
			member.Addresses = seed.Addresses
			s.Members[id] = member
		}
		return c, saveSessionState(filename, s)
	} else if !errors.Is(err, os.ErrNotExist) {
		return c, err
	}
	s := &sessionState{Version: 4, Session: c.ID, Device: pin, Root: folder, Files: map[string][]fileVersion{}, Applied: map[string]appliedFile{}, Members: members}
	return c, saveSessionState(filename, s)
}

type sessionWire struct {
	Protocol int                      `json:"protocol"`
	Session  string                   `json:"session"`
	Self     sessionMember            `json:"self"`
	Members  map[string]sessionMember `json:"members"`
	Files    map[string][]fileVersion `json:"files"`
	Contents map[string]fileInfo      `json:"contents"`
}

type sessionHello struct {
	Self    sessionMember            `json:"self"`
	Members map[string]sessionMember `json:"members"`
}

type sessionRuntime struct {
	tunnelMu         sync.Mutex
	reverse          map[string]*reverseTransport
	mu               sync.Mutex
	viewMu           sync.RWMutex
	memberView       []MemberStatus
	state            *sessionState
	filename         string
	cfg              Options
	cert             tls.Certificate
	self             sessionMember
	scanner          *scanner
	server           *http.Server
	ctx              context.Context
	cancel           context.CancelFunc
	done             chan struct{}
	wake             func()
	memberStatus     map[string]MemberStatus
	nextTry          map[string]time.Time
	changes          *host
	signature        [32]byte
	monitors         map[string]context.CancelFunc
	monitorAddresses map[string]string
	workers          sync.WaitGroup
	gate             sync.Mutex
	closing          bool
	handlers         sync.WaitGroup
}

// MemberStatus is a detached view. Offline is not permission to delete history.
type MemberStatus struct {
	ID       string    `json:"id"`
	Name     string    `json:"name"`
	Local    bool      `json:"local"`
	State    string    `json:"state"`
	LastSeen time.Time `json:"lastSeen,omitempty"`
	Error    string    `json:"error,omitempty"`
}

func (s *sessionRuntime) peer(address, pin string) *peer {
	p := newPeer(invitation{Addr: address, Token: s.cfg.Secret, Fingerprint: pin})
	transport := p.client.Transport.(*http.Transport)
	transport.TLSClientConfig.Certificates = []tls.Certificate{s.cert}
	transport.DialContext = (&net.Dialer{Timeout: 2 * time.Second, KeepAlive: 30 * time.Second}).DialContext
	p.client.CheckRedirect = func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }
	p.headers = map[string]string{"X-FolderBridge-Session": s.cfg.Session, "X-FolderBridge-Device": s.self.ID}
	p.batchGetEndpoint = "/v4/batch/get"
	return p
}

func (s *sessionRuntime) request(ctx context.Context, p *peer, method, endpoint string, body io.Reader) (*http.Response, error) {
	headers := map[string]string{"Content-Type": "application/json"}
	if u, err := url.Parse(endpoint); err == nil && u.Query().Get("hash") != "" {
		headers["X-Expected-Hash"] = u.Query().Get("hash")
	}
	return p.request(ctx, method, endpoint, body, headers)
}

func decodeSessionJSON(r io.Reader, out any) error {
	dec := json.NewDecoder(io.LimitReader(r, (64<<20)+1))
	if err := dec.Decode(out); err != nil {
		return err
	}
	var extra any
	if err := dec.Decode(&extra); err != io.EOF {
		return errors.New("invalid or oversized session message")
	}
	return nil
}

func (s *sessionRuntime) handler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("POST /v4/reverse", s.acceptReverse)
	mux.HandleFunc("POST /v4/exchange", func(w http.ResponseWriter, r *http.Request) {
		var hello sessionHello
		if err := decodeSessionJSON(http.MaxBytesReader(w, r.Body, 64<<20), &hello); err != nil {
			http.Error(w, err.Error(), 400)
			return
		}
		if hello.Self.ID != r.Header.Get("X-FolderBridge-Device") || validateMember(hello.Self) != nil {
			http.Error(w, "invalid member identity", 403)
			return
		}
		s.mu.Lock()
		defer s.mu.Unlock()
		before := len(s.state.Members)
		if err := s.learnMembers(hello.Self, hello.Members); err != nil {
			http.Error(w, err.Error(), 400)
			return
		}
		wire, err := s.snapshot(false)
		if err != nil {
			http.Error(w, err.Error(), 409)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(wire)
		if len(s.state.Members) > before {
			s.wake()
		}
	})
	// Reads never wait for the reconciliation lock: two members may be fetching
	// from one another simultaneously. Expected hashes reject live-file races.
	mux.HandleFunc("GET /v4/file", s.changes.readFile)
	mux.HandleFunc("POST /v4/batch/get", s.changes.batchGet)
	mux.HandleFunc("GET /v4/changes", s.changes.waitForChanges)
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		s.gate.Lock()
		if s.closing {
			s.gate.Unlock()
			http.Error(w, "session stopping", 503)
			return
		}
		s.handlers.Add(1)
		s.gate.Unlock()
		defer s.handlers.Done()
		if subtle.ConstantTimeCompare([]byte(r.Header.Get("Authorization")), []byte("Bearer "+s.cfg.Secret)) != 1 || r.Header.Get("X-FolderBridge-Session") != s.cfg.Session {
			http.Error(w, "unauthorized session", 401)
			return
		}
		if r.TLS == nil || len(r.TLS.PeerCertificates) != 1 {
			http.Error(w, "device certificate required", 403)
			return
		}
		pin := sha256.Sum256(r.TLS.PeerCertificates[0].Raw)
		if hex.EncodeToString(pin[:]) != r.Header.Get("X-FolderBridge-Device") {
			http.Error(w, "device identity mismatch", 403)
			return
		}
		w.Header().Set("Cache-Control", "no-store")
		mux.ServeHTTP(w, r)
	})
}

func (s *sessionRuntime) learnMembers(self sessionMember, members map[string]sessionMember) error {
	if err := validateMember(self); err != nil {
		return err
	}
	for id, m := range members {
		if id != m.ID || validateMember(m) != nil {
			return errors.New("invalid member roster")
		}
	}
	// Directly authenticated self-description wins over forwarded information.
	for id, m := range members {
		if id == s.self.ID {
			continue
		}
		old, ok := s.state.Members[id]
		if !ok || m.Revision > old.Revision {
			s.state.Members[id] = m
		}
	}
	if self.ID != s.self.ID && self.Revision >= s.state.Members[self.ID].Revision {
		s.state.Members[self.ID] = self
	}
	s.publishMembers()
	return nil
}

func newSessionRuntime(home string, cfg Options, address string, wake func()) (*sessionRuntime, error) {
	if !validHex(cfg.Session, 16) || !validHex(cfg.Secret, 24) {
		return nil, errors.New("create or join a new sharing session first")
	}
	if err := fileops.CheckSyncRoot(cfg.Folder); err != nil {
		return nil, err
	}
	cert, pin, err := newTLSCert(filepath.Join(home, "peer-cert.pem"), filepath.Join(home, "peer-key.pem"))
	if err != nil {
		return nil, err
	}
	filename := filepath.Join(home, "session-"+cfg.Session+".json")
	state, err := loadSessionState(filename, cfg.Session, pin, cfg.Folder)
	if err != nil {
		return nil, fmt.Errorf("session history unavailable (not reset): %w", err)
	}
	ln, err := net.Listen("tcp", address)
	if err != nil {
		return nil, err
	}
	_, port, _ := net.SplitHostPort(ln.Addr().String())
	addresses := []string{}
	host, _, _ := net.SplitHostPort(address)
	if ip := net.ParseIP(host); ip != nil && !ip.IsUnspecified() {
		addresses = append(addresses, net.JoinHostPort(host, port))
	} else {
		for _, addr := range lanAddresses() {
			candidate := net.JoinHostPort(addr, port)
			if validLANAddress(candidate) {
				addresses = append(addresses, candidate)
			}
		}
	}
	name := strings.TrimSpace(cfg.Name)
	if name == "" {
		name, _ = os.Hostname()
	}
	if len(name) > 128 {
		name = name[:128]
	}
	revision := state.Members[pin].Revision + 1
	if revision == 0 {
		ln.Close()
		return nil, errors.New("device incarnation exhausted; fresh pairing required")
	}
	ctx, cancel := context.WithCancel(context.Background())
	s := &sessionRuntime{state: state, filename: filename, cfg: cfg, cert: cert, self: sessionMember{ID: pin, Name: name, Addresses: addresses, Revision: revision}, scanner: newPersistentScanner(cfg.Folder, home), ctx: ctx, cancel: cancel, done: make(chan struct{}), wake: wake, memberStatus: map[string]MemberStatus{}, nextTry: map[string]time.Time{}}
	s.changes = newHost(cfg.Folder, cfg.Secret)
	s.changes.scanner = s.scanner
	s.monitors = map[string]context.CancelFunc{}
	s.monitorAddresses = map[string]string{}
	state.Members[pin] = s.self
	s.publishMembers()
	if err := saveSessionState(filename, state); err != nil {
		ln.Close()
		cancel()
		return nil, err
	}
	s.server = &http.Server{Handler: s.handler(), ReadHeaderTimeout: 10 * time.Second, BaseContext: func(net.Listener) context.Context { return ctx }}
	go func() {
		_ = s.server.Serve(tls.NewListener(ln, &tls.Config{Certificates: []tls.Certificate{cert}, MinVersion: tls.VersionTLS13, ClientAuth: tls.RequireAnyClientCert}))
	}()
	return s, nil
}
