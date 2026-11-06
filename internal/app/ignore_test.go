package app

import (
	"encoding/json"
	"folderbridge/internal/syncengine"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
)

func TestIgnoreInspectorLocalEndpoint(t *testing.T) {
	root := t.TempDir()
	writeTestFile(t, root, ".fbignore", "secret.txt\n")
	a, err := newApp(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	a.cfg = config{Mode: "session", Folder: root, Enabled: true}
	rec := httptest.NewRecorder()
	a.uiHandler().ServeHTTP(rec, httptest.NewRequest("GET", "http://"+uiAddress+"/api/ignore?path=secret.txt", nil))
	var out syncengine.IgnoreExplanation
	if rec.Code != 200 {
		t.Fatal(rec.Code, rec.Body.String())
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil {
		t.Fatal(err)
	}
	if !out.Local.Ignored || !out.Effective || out.Local.Source != ".fbignore" || out.Local.Line != 1 {
		t.Fatalf("wrong inspector %+v", out)
	}
}

func TestIgnoreInspectorDoesNotFetchPeerRules(t *testing.T) {
	root := t.TempDir()
	writeTestFile(t, root, ".fbignore", "local-only.txt\n")
	var requests atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests.Add(1)
		http.Error(w, "must not fetch remote rules", 500)
	}))
	defer srv.Close()
	a, err := newApp(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	a.cfg = config{Mode: "session", Folder: root, Invite: srv.URL, Enabled: true}
	for _, name := range []string{"remote-only.txt", "local-only.txt"} {
		rec := httptest.NewRecorder()
		a.uiHandler().ServeHTTP(rec, httptest.NewRequest("GET", "http://"+uiAddress+"/api/ignore?path="+name, nil))
		var out syncengine.IgnoreExplanation
		if rec.Code != 200 {
			t.Fatal(rec.Code, rec.Body.String())
		}
		if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil {
			t.Fatal(err)
		}
		if out.Effective != (name == "local-only.txt") || out.Remote != nil || !strings.Contains(out.RemoteStatus, "Device-local") {
			t.Fatal(name, out)
		}
	}
	if requests.Load() != 0 {
		t.Fatal("inspector fetched remote rules")
	}
}

func TestIgnoreInspectorRequiresFreshSession(t *testing.T) {
	a, err := newApp(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	a.cfg = config{Mode: "join", Folder: t.TempDir(), Enabled: true}
	rec := httptest.NewRecorder()
	a.uiHandler().ServeHTTP(rec, httptest.NewRequest("GET", "http://"+uiAddress+"/api/ignore?path=file.txt", nil))
	if rec.Code != 409 {
		t.Fatal("legacy settings treated as a session", rec.Code)
	}
}
