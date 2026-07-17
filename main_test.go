package main

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func withTempIPDir(t *testing.T) string {
	t.Helper()
	old := ipDir
	ipDir = t.TempDir()
	t.Cleanup(func() { ipDir = old })
	return ipDir
}

func reportRequest(hostname, localIP string) *http.Request {
	r := httptest.NewRequest(http.MethodGet, "/api/host/report?hostname="+hostname+"&local_ip="+localIP, nil)
	r.Header.Set("X-Forwarded-For", "203.0.113.8")
	return r
}

func TestHostReportValidation(t *testing.T) {
	withTempIPDir(t)
	tests := []*http.Request{
		reportRequest("../etc", "192.168.1.2"),
		reportRequest("host", "192.168.1.999"),
		reportRequest("bad_host", "192.168.1.2"),
	}
	for _, req := range tests {
		w := httptest.NewRecorder()
		apiHostReport(w, req)
		if w.Code != http.StatusBadRequest {
			t.Fatalf("%s: got status %d, want 400", req.URL.RawQuery, w.Code)
		}
	}
}

func TestHostReportReplacesSameHostAndRemovesOldFiles(t *testing.T) {
	dir := withTempIPDir(t)
	oldSameHost := filepath.Join(dir, "nas-198.51.100.1-10.0.0.1")
	oldOtherHost := filepath.Join(dir, "old-198.51.100.2-10.0.0.2")
	if err := os.WriteFile(oldSameHost, nil, 0640); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(oldOtherHost, nil, 0640); err != nil {
		t.Fatal(err)
	}
	oldTime := time.Now().Add(-31 * 24 * time.Hour)
	if err := os.Chtimes(oldOtherHost, oldTime, oldTime); err != nil {
		t.Fatal(err)
	}

	w := httptest.NewRecorder()
	apiHostReport(w, reportRequest("nas", "192.168.1.2,10.0.0.3"))
	if w.Code != http.StatusOK {
		t.Fatalf("got status %d: %s", w.Code, w.Body.String())
	}

	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 1 || entries[0].Name() != "nas-203.0.113.8-192.168.1.2-10.0.0.3" {
		t.Fatalf("unexpected files: %v", entries)
	}
}

func TestHostsNewestFirst(t *testing.T) {
	dir := withTempIPDir(t)
	older := filepath.Join(dir, "older-203.0.113.1-10.0.0.1")
	newer := filepath.Join(dir, "newer-203.0.113.2-10.0.0.2")
	if err := os.WriteFile(older, nil, 0640); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(newer, nil, 0640); err != nil {
		t.Fatal(err)
	}
	now := time.Now()
	if err := os.Chtimes(older, now.Add(-time.Hour), now.Add(-time.Hour)); err != nil {
		t.Fatal(err)
	}
	if err := os.Chtimes(newer, now, now); err != nil {
		t.Fatal(err)
	}

	w := httptest.NewRecorder()
	apiHosts(w, httptest.NewRequest(http.MethodGet, "/api/hosts", nil))
	var names []string
	if err := json.Unmarshal(w.Body.Bytes(), &names); err != nil {
		t.Fatal(err)
	}
	if len(names) != 2 || names[0] != filepath.Base(newer) || names[1] != filepath.Base(older) {
		t.Fatalf("unexpected order: %v", names)
	}
}

func TestEmbeddedVueRuntime(t *testing.T) {
	data, err := staticFS.ReadFile("static/vue.global.prod.js")
	if err != nil {
		t.Fatal(err)
	}
	if len(data) < 100000 {
		t.Fatalf("embedded Vue runtime is unexpectedly small: %d bytes", len(data))
	}
}
