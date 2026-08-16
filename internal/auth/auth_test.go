package auth

import (
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestBearer(t *testing.T) {
	h := Bearer("secret", http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusNoContent) }))
	for _, tc := range []struct {
		header string
		want   int
	}{{"Bearer secret", 204}, {"Bearer wrong", 401}, {"", 401}, {"Basic secret", 401}} {
		r := httptest.NewRequest("GET", "/", nil)
		r.Header.Set("Authorization", tc.header)
		w := httptest.NewRecorder()
		h.ServeHTTP(w, r)
		if w.Code != tc.want {
			t.Fatalf("header %q: got %d want %d", tc.header, w.Code, tc.want)
		}
	}
}
