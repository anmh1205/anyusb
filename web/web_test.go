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

func TestDesktopUIOnlyOffersNativeUSBForwarding(t *testing.T) {
	index, err := files.ReadFile("static/index.html")
	if err != nil {
		t.Fatal(err)
	}
	script, err := files.ReadFile("static/app.js")
	if err != nil {
		t.Fatal(err)
	}
	page := strings.ToLower(string(index) + string(script))
	for _, forbidden := range []string{"type=\"file\"", "firmware", "công cụ esp", "chọn file"} {
		if strings.Contains(page, forbidden) {
			t.Fatalf("desktop UI contains obsolete firmware workflow %q", forbidden)
		}
	}
	for _, required := range []string{
		"cổng trên máy này", "usb đã được gắn vào hệ điều hành", "thiết bị này không cần com/tty", "liên kết hệ điều hành",
		"driver_missing", "confirm_risk", "showemulist usb", "stm32cubeprogrammer", "pyocd", "tự nối lại",
	} {
		if !strings.Contains(page, required) {
			t.Fatalf("desktop UI is missing native forwarding guidance %q", required)
		}
	}
}
