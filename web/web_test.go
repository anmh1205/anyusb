package web

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestLoopbackAuthorization(t *testing.T) {
	u := New("127.0.0.1:47440")
	u.API = http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Host != "anyusb" || r.Header.Get("Origin") != "" || r.Header.Get("Authorization") != "" {
			t.Error("UI did not isolate browser credentials from the local API")
		}
		w.WriteHeader(http.StatusNoContent)
	})
	for _, sc := range []struct {
		path, origin, bearer string
		want                 int
	}{
		{"/", "", "", http.StatusOK},
		{"/app.js", "", "", http.StatusOK},
		{"/v1/devices", "", "", http.StatusUnauthorized},
		{"/v1/devices", "http://irrelevant.local", "Bearer " + u.token, http.StatusForbidden},
		{"/v1/devices", "http://127.0.0.1:47440", "Bearer " + u.token, http.StatusNoContent},
	} {
		request := httptest.NewRequest(http.MethodGet, "http://127.0.0.1:47440"+sc.path, nil)
		request.Header.Set("Origin", sc.origin)
		request.Header.Set("Authorization", sc.bearer)
		recorder := httptest.NewRecorder()
		u.ServeHTTP(recorder, request)
		if recorder.Code != sc.want {
			t.Errorf("%s: status %d expected %d", sc.path, recorder.Code, sc.want)
		}
		if sc.path == "/" && !strings.Contains(recorder.Body.String(), "<title>AnyUSB</title>") {
			t.Error("missing embedded UI")
		}
	}
}
