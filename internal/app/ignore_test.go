package app

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"folderbridge/internal/syncengine"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestIgnoreInspectorLocalEndpoint(t *testing.T) {
	root := t.TempDir()
	writeTestFile(t, root, ".folderbridgeignore", "secret.txt\n")
	a, err := newApp(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	a.cfg = config{Mode: "host", Folder: root, Enabled: true}
	rec := httptest.NewRecorder()
	a.uiHandler().ServeHTTP(rec, httptest.NewRequest("GET", "http://"+uiAddress+"/api/ignore?path=secret.txt", nil))
	if rec.Code != 200 {
		t.Fatalf("status %d body %s", rec.Code, rec.Body.String())
	}
	var out syncengine.IgnoreExplanation
	if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil {
		t.Fatal(err)
	}
	if !out.Local.Ignored || !out.Effective || out.Local.Source != ".folderbridgeignore" || out.Local.Line != 1 {
		t.Fatalf("wrong inspector %+v", out)
	}
}
func TestIgnoreInspectorCombinesPeerRules(t *testing.T) {
	root := t.TempDir()
	writeTestFile(t, root, ".folderbridgeignore", "local-only.txt\n")
	token := strings.Repeat("a", 48)
	srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v2/manifest" || r.Header.Get("Authorization") != "Bearer "+token {
			http.Error(w, "unexpected peer request", 400)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(`{"files":{},"ignoreFiles":[{"path":".gitignore","text":"server-only.txt\n"}]}`))
	}))
	defer srv.Close()
	pin := sha256.Sum256(srv.TLS.Certificates[0].Certificate[0])
	invite := "folderbridge://" + strings.TrimPrefix(srv.URL, "https://") + "/" + token + "?fp=" + hex.EncodeToString(pin[:])
	a, err := newApp(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	a.cfg = config{Mode: "join", Folder: root, Invite: invite, Enabled: true}
	for _, path := range []string{"server-only.txt", "local-only.txt"} {
		rec := httptest.NewRecorder()
		a.uiHandler().ServeHTTP(rec, httptest.NewRequest("GET", "http://"+uiAddress+"/api/ignore?path="+path, nil))
		if rec.Code != 200 {
			t.Fatalf("%s HTTP %d: %s", path, rec.Code, rec.Body.String())
		}
		var out syncengine.IgnoreExplanation
		if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil {
			t.Fatal(err)
		}
		if !out.Effective || out.Remote == nil || out.RemoteStatus != "Available" {
			t.Fatalf("%s: %+v", path, out)
		}
		if path == "server-only.txt" && !out.Remote.Ignored {
			t.Fatal("missing peer rule", out)
		}
		if path == "local-only.txt" && !out.Local.Ignored {
			t.Fatal("missing local rule", out)
		}
	}
}
func TestIgnoreInspectorInvalidJoinStillReportsRemoteUnavailable(t *testing.T) {
	a, err := newApp(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	a.cfg = config{Mode: "join", Folder: t.TempDir(), Enabled: true}
	rec := httptest.NewRecorder()
	a.uiHandler().ServeHTTP(rec, httptest.NewRequest("GET", "http://"+uiAddress+"/api/ignore?path=file.txt", nil))
	var out syncengine.IgnoreExplanation
	if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil {
		t.Fatal(err)
	}
	if rec.Code != 200 || out.RemoteStatus != "Unavailable" || out.Remote != nil {
		t.Fatalf("unexpected unavailable peer response: %d %+v", rec.Code, out)
	}
}
