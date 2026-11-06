package app

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestDashboardRejectsForeignAuthority(t *testing.T) {
	a, err := newApp(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	a.cfg = config{Mode: "join", Enabled: true, Invite: "REVIEW-PLACEHOLDER-PRIVATE-INVITE"}
	for _, host := range []string{"untrusted.example:47831", "localhost:47831", "127.0.0.1:9999", "127.0.0.1:47831.evil.example", ""} {
		for _, endpoint := range []string{"/", "/api/status", "/api/ignore?path=test.txt", "/api/quit"} {
			t.Run(host+endpoint, func(t *testing.T) {
				method := http.MethodGet
				if endpoint == "/api/quit" {
					method = http.MethodPost
				}
				req := httptest.NewRequest(method, endpoint, strings.NewReader(`{}`))
				req.Host = host
				req.Header.Set("Content-Type", "application/json")
				// GETs must be protected even without Origin; POSTs must not
				// bypass the Host check with an otherwise allowed Origin.
				if method == http.MethodPost {
					req.Header.Set("Origin", "http://"+uiAddress)
				}
				rec := httptest.NewRecorder()
				a.uiHandler().ServeHTTP(rec, req)
				if rec.Code != http.StatusForbidden {
					t.Fatalf("got %d, want 403", rec.Code)
				}
				if strings.Contains(rec.Body.String(), a.cfg.Invite) {
					t.Fatal("pairing invite disclosed")
				}
				select {
				case <-a.quit:
					t.Fatal("rejected request executed quit action")
				default:
				}
			})
		}
	}
}

func TestDashboardLocalAuthorityAndOrigin(t *testing.T) {
	a, err := newApp(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	for _, endpoint := range []string{"/", "/api/status"} {
		req := httptest.NewRequest(http.MethodGet, "http://"+uiAddress+endpoint, nil)
		rec := httptest.NewRecorder()
		a.uiHandler().ServeHTTP(rec, req)
		if rec.Code != http.StatusOK {
			t.Fatalf("%s: got %d", endpoint, rec.Code)
		}
	}
	for _, origin := range []string{"http://untrusted.example", "null"} {
		req := httptest.NewRequest(http.MethodPost, "http://"+uiAddress+"/api/quit", strings.NewReader(`{}`))
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("Origin", origin)
		rec := httptest.NewRecorder()
		a.uiHandler().ServeHTTP(rec, req)
		if rec.Code != http.StatusForbidden {
			t.Fatalf("origin %q: got %d", origin, rec.Code)
		}
	}
	req := httptest.NewRequest(http.MethodPost, "http://"+uiAddress+"/api/sync", strings.NewReader(`{}`))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Origin", "http://"+uiAddress)
	rec := httptest.NewRecorder()
	a.uiHandler().ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("local action: got %d", rec.Code)
	}
}
