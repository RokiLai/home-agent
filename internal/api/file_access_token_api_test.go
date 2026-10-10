package api

import (
	"bytes"
	"encoding/json"
	"homeagent/internal/auth"
	"homeagent/internal/fileaccess"
	"homeagent/internal/fileshare"
	"io"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// Uses the real local HTTP server and existing product contracts. No external protocol mock.
func TestFileAccessTokenHTTPJourney(t *testing.T) {
	sm, _ := auth.NewSessionManager("")
	sm.InitAdminBootstrap("owner", "StrongPass123!")
	cookie := sessionCookieFor(t, sm, "owner", "StrongPass123!")
	svc, err := fileaccess.Open(filepath.Join(t.TempDir(), "tokens.json"), fileTokenUserLookup(sm), time.Now)
	if err != nil {
		t.Fatal(err)
	}
	fs, err := fileshare.Open(t.TempDir(), fileshare.Options{QuotaBytes: 1024, DiskAvailable: func(string) (int64, error) { return 1 << 30, nil }})
	if err != nil {
		t.Fatal(err)
	}
	ts := httptest.NewServer((&Server{SessionManager: sm, FileAccess: svc, FileShare: fs}).Handler())
	defer ts.Close()
	call := func(method, path, token string, body io.Reader, withCookie bool) (int, http.Header, []byte) {
		t.Helper()
		req, _ := http.NewRequest(method, ts.URL+path, body)
		if token != "" {
			req.Header.Set("Authorization", "Bearer "+token)
		}
		if withCookie {
			req.AddCookie(cookie)
		}
		res, e := ts.Client().Do(req)
		if e != nil {
			t.Fatal(e)
		}
		defer res.Body.Close()
		b, _ := io.ReadAll(res.Body)
		return res.StatusCode, res.Header, b
	}
	status, header, b := call("POST", "/api/v1/file-access-tokens", "", strings.NewReader(`{"name":"phone"}`), true)
	if status != 201 || header.Get("Cache-Control") != "no-store" {
		t.Fatalf("create %d %s", status, b)
	}
	var created struct {
		fileaccess.Record
		Token string `json:"token"`
	}
	if json.Unmarshal(b, &created) != nil || created.Token == "" {
		t.Fatal(string(b))
	}
	status, _, b = call("GET", "/api/v1/file-access-tokens", "", nil, true)
	if status != 200 || bytes.Contains(b, []byte(created.Token)) || bytes.Contains(b, []byte("token_hash")) {
		t.Fatal("list leaked")
	}
	for _, route := range []struct{ method, path string }{{"DELETE", "/api/v1/files/fake"}, {"GET", "/api/v1/files/settings"}, {"PUT", "/api/v1/files/settings"}, {"GET", "/api/v1/files/fake/links"}, {"DELETE", "/api/v1/files/fake/links/fake"}, {"GET", "/api/v1/devices"}, {"GET", "/api/v1/users"}, {"GET", "/api/v1/file-access-tokens"}, {"POST", "/api/v1/file-access-tokens"}, {"DELETE", "/api/v1/file-access-tokens/" + created.ID}} {
		status, _, _ = call(route.method, route.path, created.Token, nil, true)
		if status != 403 {
			t.Fatalf("cookie fallback %s %s=%d", route.method, route.path, status)
		}
	}
	status, _, _ = call("GET", "/api/v1/files", "agt_file_bad", nil, true)
	if status != 401 {
		t.Fatal("invalid fallback", status)
	}
	status, _, _ = call("GET", "/api/v1/files?token="+created.Token, "", nil, true)
	if status != 401 {
		t.Fatal("query accepted", status)
	}
	req, _ := http.NewRequest("GET", ts.URL+"/api/v1/files", nil)
	req.AddCookie(&http.Cookie{Name: auth.SessionCookieName, Value: created.Token})
	res, _ := ts.Client().Do(req)
	res.Body.Close()
	if res.StatusCode != 401 {
		t.Fatal("cookie token accepted")
	}
	var body bytes.Buffer
	mw := multipart.NewWriter(&body)
	part, _ := mw.CreateFormFile("file", "test.txt")
	part.Write([]byte("abcdef"))
	mw.Close()
	req, _ = http.NewRequest("POST", ts.URL+"/api/v1/files", &body)
	req.Header.Set("Authorization", "Bearer "+created.Token)
	req.Header.Set("Content-Type", mw.FormDataContentType())
	res, err = ts.Client().Do(req)
	if err != nil {
		t.Fatal(err)
	}
	b, _ = io.ReadAll(res.Body)
	res.Body.Close()
	if res.StatusCode != 201 {
		t.Fatalf("upload %d %s", res.StatusCode, b)
	}
	var uploaded fileshare.FileRecord
	json.Unmarshal(b, &uploaded)
	status, _, b = call("GET", "/api/v1/files", created.Token, nil, false)
	if status != 200 || !bytes.Contains(b, []byte(uploaded.ID)) {
		t.Fatal("list")
	}
	req, _ = http.NewRequest("GET", ts.URL+"/api/v1/files/"+uploaded.ID+"/download", nil)
	req.Header.Set("Authorization", "Bearer "+created.Token)
	req.Header.Set("Range", "bytes=1-3")
	res, _ = ts.Client().Do(req)
	b, _ = io.ReadAll(res.Body)
	res.Body.Close()
	if res.StatusCode != 206 || string(b) != "bcd" {
		t.Fatal("range")
	}
	status, _, b = call("POST", "/api/v1/files/"+uploaded.ID+"/links", created.Token, strings.NewReader(`{"expires_in_seconds":3600}`), false)
	if status != 201 {
		t.Fatalf("link %d %s", status, b)
	}
	var link struct {
		URL string `json:"download_url"`
	}
	json.Unmarshal(b, &link)
	publicPath := strings.TrimPrefix(link.URL, "https://homeagent.rokilai.online")
	status, _, _ = call("DELETE", "/api/v1/file-access-tokens/"+created.ID, "", nil, true)
	if status != 204 {
		t.Fatal("revoke")
	}
	status, _, _ = call("GET", "/api/v1/files", created.Token, nil, true)
	if status != 401 {
		t.Fatal("revoked fallback")
	}
	status, _, b = call("GET", publicPath, "", nil, false)
	if status != 200 || string(b) != "abcdef" {
		t.Fatal("public link revoked incorrectly")
	}
	status, _, _ = call("DELETE", "/api/v1/file-access-tokens/"+created.ID, "", nil, true)
	if status != 204 {
		t.Fatal("repeat revoke")
	}
	status, _, _ = call("DELETE", "/api/v1/file-access-tokens/missing", "", nil, true)
	if status != 404 {
		t.Fatal("missing")
	}
}

func TestFileTokenUnavailableAndInvalidRequests(t *testing.T) {
	sm, _ := auth.NewSessionManager("")
	sm.InitAdminBootstrap("owner", "StrongPass123!")
	cookie := sessionCookieFor(t, sm, "owner", "StrongPass123!")
	s := &Server{SessionManager: sm}
	h := s.Handler()
	for _, method := range []string{"GET", "POST", "DELETE"} {
		path := "/api/v1/file-access-tokens"
		if method == "DELETE" {
			path += "/x"
		}
		r := httptest.NewRequest(method, path, nil)
		r.AddCookie(cookie)
		w := httptest.NewRecorder()
		h.ServeHTTP(w, r)
		if w.Code != 503 {
			t.Fatal(w.Code)
		}
	}
	svc, _ := fileaccess.Open("", fileTokenUserLookup(sm), time.Now)
	s.FileAccess = svc
	h = s.Handler()
	for _, body := range []string{`{`, `{"name":""}`, `{"name":"x","expires_in_days":1}`, `{"name":"x","scopes":["all"]}`, `{"name":"x"} {}`} {
		r := httptest.NewRequest("POST", "/api/v1/file-access-tokens", strings.NewReader(body))
		r.AddCookie(cookie)
		w := httptest.NewRecorder()
		h.ServeHTTP(w, r)
		if w.Code != 400 {
			t.Fatalf("%s %d", body, w.Code)
		}
	}
}

type fileTokenPausedReader struct {
	data    *bytes.Reader
	entered chan struct{}
	release chan struct{}
	paused  bool
}

func (p *fileTokenPausedReader) Read(b []byte) (int, error) {
	if !p.paused {
		p.paused = true
		close(p.entered)
		<-p.release
	}
	return p.data.Read(b)
}
func TestFileTokenRevokedInFlightUpload(t *testing.T) {
	sm, _ := auth.NewSessionManager("")
	sm.InitAdminBootstrap("owner", "StrongPass123!")
	u, _ := sm.AuthenticateUser("owner", "StrongPass123!")
	svc, _ := fileaccess.Open("", fileTokenUserLookup(sm), time.Now)
	token, raw, _ := svc.Create(u.ID, "phone", 365)
	fs, _ := fileshare.Open(t.TempDir(), fileshare.Options{QuotaBytes: 1024, DiskAvailable: func(string) (int64, error) { return 1 << 30, nil }})
	h := (&Server{SessionManager: sm, FileAccess: svc, FileShare: fs}).Handler()
	var body bytes.Buffer
	mw := multipart.NewWriter(&body)
	part, _ := mw.CreateFormFile("file", "test.txt")
	part.Write([]byte("abcdef"))
	mw.Close()
	paused := &fileTokenPausedReader{data: bytes.NewReader(body.Bytes()), entered: make(chan struct{}), release: make(chan struct{})}
	req := httptest.NewRequest("POST", "/api/v1/files", paused)
	req.Header.Set("Content-Type", mw.FormDataContentType())
	req.Header.Set("Authorization", "Bearer "+raw)
	w := httptest.NewRecorder()
	done := make(chan struct{})
	go func() { h.ServeHTTP(w, req); close(done) }()
	<-paused.entered
	if err := svc.Revoke(u.ID, token.ID); err != nil {
		t.Fatal(err)
	}
	close(paused.release)
	<-done
	if w.Code != 401 {
		t.Fatalf("inflight=%d %s", w.Code, w.Body.String())
	}
	files, usage := fs.List()
	if len(files) != 0 || usage.UsedBytes != 0 || usage.ReservedBytes != 0 {
		t.Fatal("revoked upload left state")
	}
}
