package api

import (
	"bytes"
	"encoding/json"
	"io"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"homeagent/internal/auth"
	"homeagent/internal/fileshare"
)

func newFileShareAPI(t *testing.T) (http.Handler, *auth.SessionManager, *fileshare.Service) {
	t.Helper()
	sm, err := auth.NewSessionManager("")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := sm.InitAdminBootstrap("owner", "StrongPass123!"); err != nil {
		t.Fatal(err)
	}
	fs, err := fileshare.Open(t.TempDir(), fileshare.Options{QuotaBytes: 1024 * 1024, DiskAvailable: func(string) (int64, error) { return 1 << 30, nil }})
	if err != nil {
		t.Fatal(err)
	}
	h := (&Server{SessionManager: sm, FileShare: fs, PublicURL: "https://home.test"}).Handler()
	return h, sm, fs
}

func sessionCookieFor(t *testing.T, sm *auth.SessionManager, username, password string) *http.Cookie {
	t.Helper()
	u, err := sm.AuthenticateUser(username, password)
	if err != nil {
		t.Fatal(err)
	}
	token, _, err := sm.CreateUserSession(u.ID, false)
	if err != nil {
		t.Fatal(err)
	}
	return &http.Cookie{Name: auth.SessionCookieName, Value: token}
}

func uploadRequest(t *testing.T, target, name, body string) *http.Request {
	t.Helper()
	var payload bytes.Buffer
	w := multipart.NewWriter(&payload)
	part, err := w.CreateFormFile("file", name)
	if err != nil {
		t.Fatal(err)
	}
	_, _ = io.WriteString(part, body)
	if err := w.Close(); err != nil {
		t.Fatal(err)
	}
	req := httptest.NewRequest(http.MethodPost, target, &payload)
	req.Header.Set("Content-Type", w.FormDataContentType())
	return req
}

func TestFileShareAuthenticatedLifecycleAndPublicLink(t *testing.T) {
	h, sm, _ := newFileShareAPI(t)
	cookie := sessionCookieFor(t, sm, "owner", "StrongPass123!")

	unauth := httptest.NewRecorder()
	h.ServeHTTP(unauth, uploadRequest(t, "/api/v1/files", "secret.txt", "abcdef"))
	if unauth.Code != http.StatusUnauthorized {
		t.Fatalf("unauth status=%d", unauth.Code)
	}

	upload := uploadRequest(t, "/api/v1/files", "报告.txt", "abcdef")
	upload.AddCookie(cookie)
	uploadRec := httptest.NewRecorder()
	h.ServeHTTP(uploadRec, upload)
	if uploadRec.Code != http.StatusCreated {
		t.Fatalf("upload status=%d body=%s", uploadRec.Code, uploadRec.Body.String())
	}
	var uploaded fileshare.FileRecord
	if err := json.Unmarshal(uploadRec.Body.Bytes(), &uploaded); err != nil {
		t.Fatal(err)
	}

	listReq := httptest.NewRequest(http.MethodGet, "/api/v1/files", nil)
	listReq.AddCookie(cookie)
	listRec := httptest.NewRecorder()
	h.ServeHTTP(listRec, listReq)
	if listRec.Code != http.StatusOK || !strings.Contains(listRec.Body.String(), "报告.txt") {
		t.Fatalf("list=%d %s", listRec.Code, listRec.Body.String())
	}

	downloadReq := httptest.NewRequest(http.MethodGet, "/api/v1/files/"+uploaded.ID+"/download", nil)
	downloadReq.Header.Set("Range", "bytes=1-3")
	downloadReq.AddCookie(cookie)
	downloadRec := httptest.NewRecorder()
	h.ServeHTTP(downloadRec, downloadReq)
	if downloadRec.Code != http.StatusPartialContent || downloadRec.Body.String() != "bcd" || downloadRec.Header().Get("Content-Range") != "bytes 1-3/6" {
		t.Fatalf("range status=%d headers=%v body=%q", downloadRec.Code, downloadRec.Header(), downloadRec.Body.String())
	}

	linkReq := httptest.NewRequest(http.MethodPost, "/api/v1/files/"+uploaded.ID+"/links", strings.NewReader(`{"expires_in_seconds":3600}`))
	linkReq.Header.Set("Content-Type", "application/json")
	linkReq.AddCookie(cookie)
	linkRec := httptest.NewRecorder()
	h.ServeHTTP(linkRec, linkReq)
	if linkRec.Code != http.StatusCreated {
		t.Fatalf("link status=%d body=%s", linkRec.Code, linkRec.Body.String())
	}
	var link struct {
		ID          string    `json:"id"`
		DownloadURL string    `json:"download_url"`
		ExpiresAt   time.Time `json:"expires_at"`
	}
	if err := json.Unmarshal(linkRec.Body.Bytes(), &link); err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(link.DownloadURL, "https://home.test/api/v1/public/files/") {
		t.Fatalf("url=%q", link.DownloadURL)
	}
	publicPath := strings.TrimPrefix(link.DownloadURL, "https://home.test")
	publicRec := httptest.NewRecorder()
	h.ServeHTTP(publicRec, httptest.NewRequest(http.MethodGet, publicPath, nil))
	if publicRec.Code != http.StatusOK || publicRec.Body.String() != "abcdef" {
		t.Fatalf("public=%d %q", publicRec.Code, publicRec.Body.String())
	}

	revokeReq := httptest.NewRequest(http.MethodDelete, "/api/v1/files/"+uploaded.ID+"/links/"+link.ID, nil)
	revokeReq.AddCookie(cookie)
	revokeRec := httptest.NewRecorder()
	h.ServeHTTP(revokeRec, revokeReq)
	if revokeRec.Code != http.StatusNoContent {
		t.Fatalf("revoke=%d", revokeRec.Code)
	}
	publicRec = httptest.NewRecorder()
	h.ServeHTTP(publicRec, httptest.NewRequest(http.MethodGet, publicPath, nil))
	if publicRec.Code != http.StatusGone {
		t.Fatalf("revoked public=%d", publicRec.Code)
	}

	deleteReq := httptest.NewRequest(http.MethodDelete, "/api/v1/files/"+uploaded.ID, nil)
	deleteReq.AddCookie(cookie)
	deleteRec := httptest.NewRecorder()
	h.ServeHTTP(deleteRec, deleteReq)
	if deleteRec.Code != http.StatusNoContent {
		t.Fatalf("delete=%d", deleteRec.Code)
	}
}

func TestFileShareSettingsPermissionAndRevision(t *testing.T) {
	h, sm, _ := newFileShareAPI(t)
	ownerCookie := sessionCookieFor(t, sm, "owner", "StrongPass123!")
	owner, _ := sm.AuthenticateUser("owner", "StrongPass123!")
	viewer, err := sm.CreateUser("viewer", "ViewerPass123!", auth.RoleViewer, owner.ID)
	if err != nil {
		t.Fatal(err)
	}
	viewerToken, _, _ := sm.CreateUserSession(viewer.ID, false)
	viewerCookie := &http.Cookie{Name: auth.SessionCookieName, Value: viewerToken}

	getReq := httptest.NewRequest(http.MethodGet, "/api/v1/files/settings", nil)
	getReq.AddCookie(viewerCookie)
	getRec := httptest.NewRecorder()
	h.ServeHTTP(getRec, getReq)
	if getRec.Code != http.StatusOK {
		t.Fatalf("viewer get=%d", getRec.Code)
	}

	viewerPut := httptest.NewRequest(http.MethodPut, "/api/v1/files/settings", strings.NewReader(`{"retention_days":30,"revision":1}`))
	viewerPut.AddCookie(viewerCookie)
	viewerPutRec := httptest.NewRecorder()
	h.ServeHTTP(viewerPutRec, viewerPut)
	if viewerPutRec.Code != http.StatusForbidden {
		t.Fatalf("viewer put=%d", viewerPutRec.Code)
	}

	ownerPut := httptest.NewRequest(http.MethodPut, "/api/v1/files/settings", strings.NewReader(`{"retention_days":30,"revision":1}`))
	ownerPut.AddCookie(ownerCookie)
	ownerPutRec := httptest.NewRecorder()
	h.ServeHTTP(ownerPutRec, ownerPut)
	if ownerPutRec.Code != http.StatusOK || !strings.Contains(ownerPutRec.Body.String(), `"retention_days":30`) {
		t.Fatalf("owner put=%d %s", ownerPutRec.Code, ownerPutRec.Body.String())
	}

	stale := httptest.NewRequest(http.MethodPut, "/api/v1/files/settings", strings.NewReader(`{"retention_days":10,"revision":1}`))
	stale.AddCookie(ownerCookie)
	staleRec := httptest.NewRecorder()
	h.ServeHTTP(staleRec, stale)
	if staleRec.Code != http.StatusConflict {
		t.Fatalf("stale=%d", staleRec.Code)
	}
}

func TestFileShareRejectsDuplicateMultipartAndUnavailableService(t *testing.T) {
	h, sm, service := newFileShareAPI(t)
	cookie := sessionCookieFor(t, sm, "owner", "StrongPass123!")
	var payload bytes.Buffer
	w := multipart.NewWriter(&payload)
	for _, name := range []string{"one.txt", "two.txt"} {
		part, _ := w.CreateFormFile("file", name)
		_, _ = io.WriteString(part, name)
	}
	_ = w.Close()
	req := httptest.NewRequest(http.MethodPost, "/api/v1/files", &payload)
	req.Header.Set("Content-Type", w.FormDataContentType())
	req.AddCookie(cookie)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("duplicate status=%d", rec.Code)
	}
	if files, usage := service.List(); len(files) != 0 || usage.UsedBytes != 0 || usage.ReservedBytes != 0 {
		t.Fatalf("duplicate leaked state files=%+v usage=%+v", files, usage)
	}

	unavailable := (&Server{SessionManager: sm}).Handler()
	get := httptest.NewRequest(http.MethodGet, "/api/v1/files", nil)
	get.AddCookie(cookie)
	getRec := httptest.NewRecorder()
	unavailable.ServeHTTP(getRec, get)
	if getRec.Code != http.StatusServiceUnavailable {
		t.Fatalf("unavailable status=%d", getRec.Code)
	}
}

func TestFileShareRealHTTPProtocol(t *testing.T) {
	h, sm, _ := newFileShareAPI(t)
	cookie := sessionCookieFor(t, sm, "owner", "StrongPass123!")
	server := httptest.NewServer(h)
	defer server.Close()

	req := uploadRequest(t, server.URL+"/api/v1/files", "network.bin", "real-http-body")
	req.RequestURI = ""
	req.AddCookie(cookie)
	resp, err := server.Client().Do(req)
	if err != nil {
		t.Fatal(err)
	}
	body, _ := io.ReadAll(resp.Body)
	_ = resp.Body.Close()
	if resp.StatusCode != http.StatusCreated {
		t.Fatalf("upload=%d %s", resp.StatusCode, body)
	}
	var record fileshare.FileRecord
	if err := json.Unmarshal(body, &record); err != nil {
		t.Fatal(err)
	}

	download, _ := http.NewRequest(http.MethodGet, server.URL+"/api/v1/files/"+record.ID+"/download", nil)
	download.AddCookie(cookie)
	download.Header.Set("Range", "bytes=5-8")
	downloadResp, err := server.Client().Do(download)
	if err != nil {
		t.Fatal(err)
	}
	downloadBody, _ := io.ReadAll(downloadResp.Body)
	_ = downloadResp.Body.Close()
	if downloadResp.StatusCode != http.StatusPartialContent || string(downloadBody) != "http" {
		t.Fatalf("download=%d %q", downloadResp.StatusCode, downloadBody)
	}
	if downloadResp.Header.Get("Content-Disposition") == "" || downloadResp.Header.Get("X-Content-Type-Options") != "nosniff" {
		t.Fatalf("download security headers=%v", downloadResp.Header)
	}
}
