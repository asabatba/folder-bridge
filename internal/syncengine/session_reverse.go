package syncengine

import (
	"bufio"
	"context"
	"crypto/tls"
	"errors"
	"net"
	"net/http"
	"sync"
	"time"
)

const reverseProtocol = "folderbridge-reverse/1"
const reverseConnections = 2 // A long poll must not block reconciliation/file reads.

// A reverse transport carries ordinary authenticated HTTP over TLS sockets that
// the remote member dialed. There is no plaintext network hop or nested TLS.
// tunnelMu is independent of mu: installing/closing sockets never waits for a
// reconciliation that might itself be waiting for one of those sockets.
type reverseTransport struct {
	peer      *peer
	available chan net.Conn
	conns     map[*reverseConn]bool
}

type reverseConn struct {
	net.Conn
	once   sync.Once
	closed func()
}

func (c *reverseConn) Close() error {
	err := c.Conn.Close()
	c.once.Do(c.closed)
	return err
}

func (s *sessionRuntime) reversePeer(id string) *peer {
	s.tunnelMu.Lock()
	defer s.tunnelMu.Unlock()
	if r := s.reverse[id]; r != nil && len(r.conns) > 0 {
		return r.peer
	}
	return nil
}

// This endpoint is behind the same certificate-bound session authentication as
// all other handlers. No caller-supplied target is dialed (not an open proxy).
func (s *sessionRuntime) acceptReverse(w http.ResponseWriter, req *http.Request) {
	if req.Header.Get("Upgrade") != reverseProtocol || req.Header.Get("Connection") != "Upgrade" || req.ContentLength != 0 {
		http.Error(w, "reverse upgrade required", http.StatusBadRequest)
		return
	}
	hijacker, ok := w.(http.Hijacker)
	if !ok {
		http.Error(w, "HTTP/1.1 required", http.StatusBadRequest)
		return
	}
	id := req.Header.Get("X-FolderBridge-Device")
	s.tunnelMu.Lock()
	defer s.tunnelMu.Unlock()
	if s.ctx.Err() != nil {
		http.Error(w, "session stopping", 503)
		return
	}
	if s.reverse == nil {
		s.reverse = map[string]*reverseTransport{}
	}
	r := s.reverse[id]
	if r == nil {
		r = &reverseTransport{available: make(chan net.Conn, reverseConnections), conns: map[*reverseConn]bool{}}
		p := s.peer("127.0.0.1:1", id)
		p.baseURL = "http://reverse" // DialContext only returns an already pinned TLS socket.
		tr := p.client.Transport.(*http.Transport)
		tr.MaxConnsPerHost = reverseConnections
		tr.MaxIdleConnsPerHost = reverseConnections
		tr.DialContext = func(ctx context.Context, _, _ string) (net.Conn, error) {
			timer := time.NewTimer(2 * time.Second)
			defer timer.Stop()
			select {
			case conn := <-r.available:
				return conn, nil
			case <-ctx.Done():
				return nil, ctx.Err()
			case <-s.ctx.Done():
				return nil, s.ctx.Err()
			case <-timer.C:
				return nil, errors.New("waiting for member's outbound connection")
			}
		}
		r.peer = p
		s.reverse[id] = r
	}
	if len(r.conns) >= reverseConnections {
		http.Error(w, "reverse connections already open", 429)
		return
	}
	conn, rw, err := hijacker.Hijack()
	if err != nil {
		return
	}
	// Clients wait for the upgrade before sending HTTP. Reject pipelined bytes
	// rather than discarding anything buffered by the outer HTTP server.
	if rw.Reader.Buffered() != 0 {
		conn.Close()
		return
	}
	if _, err = rw.WriteString("HTTP/1.1 101 Switching Protocols\r\nConnection: Upgrade\r\nUpgrade: " + reverseProtocol + "\r\n\r\n"); err == nil {
		err = rw.Flush()
	}
	if err != nil {
		conn.Close()
		return
	}
	_ = conn.SetDeadline(time.Time{})
	wrapped := &reverseConn{Conn: conn}
	wrapped.closed = func() {
		s.tunnelMu.Lock()
		delete(r.conns, wrapped)
		s.tunnelMu.Unlock()
	}
	select {
	case r.available <- wrapped:
		r.conns[wrapped] = true
	default:
		// Closed, not-yet-consumed sockets may still occupy queue slots after
		// a disconnect. Never block the session-wide socket lock on that queue.
		_ = conn.Close()
		return
	}
	// A newly usable route should not wait for an old direct-dial backoff.
	// Do not take mu here: a reconciliation may be waiting for this connection.
	s.wake()
}

func (s *sessionRuntime) closeReverse() {
	s.tunnelMu.Lock()
	var conns []*reverseConn
	var peers []*peer
	for _, r := range s.reverse {
		peers = append(peers, r.peer)
	drain:
		for {
			select {
			case <-r.available:
			default:
				break drain
			}
		}
		for c := range r.conns {
			conns = append(conns, c)
		}
	}
	s.tunnelMu.Unlock()
	for _, c := range conns {
		_ = c.Close()
	}
	for _, p := range peers {
		p.Close()
	}
}

// Two independently reconnecting outbound sockets leave room for a long poll
// and file requests. Old v4 peers return 404 and continue using direct transport.
func (s *sessionRuntime) maintainReverse(ctx context.Context, m sessionMember) {
	for ctx.Err() == nil {
		for _, addr := range m.Addresses {
			if ctx.Err() != nil {
				return
			}
			_ = s.serveReverse(ctx, addr, m.ID)
		}
		timer := time.NewTimer(5 * time.Second)
		select {
		case <-ctx.Done():
			timer.Stop()
			return
		case <-timer.C:
		}
	}
}

func (s *sessionRuntime) serveReverse(ctx context.Context, addr, id string) error {
	p := s.peer(addr, id)
	defer p.Close()
	tlsConfig := p.client.Transport.(*http.Transport).TLSClientConfig.Clone()
	dialCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
	conn, err := (&tls.Dialer{NetDialer: &net.Dialer{Timeout: 2 * time.Second, KeepAlive: 30 * time.Second}, Config: tlsConfig}).DialContext(dialCtx, "tcp", addr)
	cancel()
	if err != nil {
		return err
	}
	defer conn.Close()
	stop := context.AfterFunc(ctx, func() { _ = conn.Close() })
	defer stop()
	_ = conn.SetDeadline(time.Now().Add(5 * time.Second))
	req, err := http.NewRequest("POST", "https://"+addr+"/v4/reverse", nil)
	if err != nil {
		return err
	}
	req.Header.Set("Authorization", "Bearer "+s.cfg.Secret)
	for k, v := range p.headers {
		req.Header.Set(k, v)
	}
	req.Header.Set("Connection", "Upgrade")
	req.Header.Set("Upgrade", reverseProtocol)
	if err = req.Write(conn); err != nil {
		return err
	}
	reader := bufio.NewReader(conn)
	resp, err := http.ReadResponse(reader, req)
	if err != nil {
		return err
	}
	if resp.StatusCode != http.StatusSwitchingProtocols || resp.Header.Get("Upgrade") != reverseProtocol {
		resp.Body.Close()
		return errors.New("member does not accept reverse connections")
	}
	// The peer can send its first HTTP request immediately after its 101 reply.
	// Preserve any bytes that ReadResponse read ahead of that request.
	_ = conn.SetDeadline(time.Time{})
	tlsConn := conn.(*tls.Conn)
	state := tlsConn.ConnectionState()
	listener := &reverseListener{conn: &bufferedReverseConn{Conn: conn, reader: reader}, done: make(chan struct{})}
	defer listener.Close()
	handler := s.handler()
	server := &http.Server{
		Handler: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			// This is the TLS state of this exact, pinned, outbound connection. The
			// wrapper preserves buffered HTTP bytes but hides *tls.Conn from net/http.
			r.TLS = &state
			if r.URL.Path == "/v4/reverse" {
				// A reverse socket serves data, not more transport upgrades.
				// Nested hijacking would detach it from this worker's lifecycle.
				http.NotFound(w, r)
				return
			}
			handler.ServeHTTP(w, r)
		}),
		ReadHeaderTimeout: 10 * time.Second,
		BaseContext:       func(net.Listener) context.Context { return ctx },
		ConnState: func(_ net.Conn, st http.ConnState) {
			if st == http.StateClosed {
				_ = listener.Close()
			}
		},
	}
	_ = server.Serve(listener)
	return nil
}

type bufferedReverseConn struct {
	net.Conn
	reader *bufio.Reader
}

func (c *bufferedReverseConn) Read(b []byte) (int, error) { return c.reader.Read(b) }

// Serve must stay alive until its single connection closes; returning an error
// on the second Accept would otherwise strand the connection's handler goroutine.
type reverseListener struct {
	conn     net.Conn
	accepted bool
	done     chan struct{}
	once     sync.Once
}

func (l *reverseListener) Accept() (net.Conn, error) {
	if !l.accepted {
		l.accepted = true
		return l.conn, nil
	}
	<-l.done
	return nil, net.ErrClosed
}
func (l *reverseListener) Close() error {
	l.once.Do(func() { _ = l.conn.Close(); close(l.done) })
	return nil
}
func (l *reverseListener) Addr() net.Addr { return l.conn.LocalAddr() }
