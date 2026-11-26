// Package web serves the embedded local administration interface.
package web

import (
	"crypto/rand"
	"crypto/subtle"
	"embed"
	"encoding/hex"
	"io/fs"
	"net/http"
	"strings"

	"github.com/anmh1205/anyusb/agent/core"
)

//go:embed static/*
var files embed.FS

type UI struct {
	host, token string
	assets      http.Handler
	API         http.Handler
}

func New(host string) *UI {
	var token [32]byte
	if _, err := rand.Read(token[:]); err != nil {
		panic(err)
	}
	filesystem, _ := fs.Sub(files, "static")
	return &UI{host: host, token: hex.EncodeToString(token[:]), assets: http.FileServerFS(filesystem)}
}

func (u *UI) URL() string { return "http://" + u.host + "/#" + u.token }

func (u *UI) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("X-Content-Type-Options", "nosniff")
	w.Header().Set("Referrer-Policy", "no-referrer")
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("Content-Security-Policy", "default-src 'self'; object-src 'none'; frame-ancestors 'none'; base-uri 'none'; connect-src 'self' ws://"+u.host)
	if r.Host != u.host || (r.Header.Get("Origin") != "" && r.Header.Get("Origin") != "http://"+u.host) {
		http.Error(w, "invalid local origin", http.StatusForbidden)
		return
	}
	if !strings.HasPrefix(r.URL.Path, "/v1/") {
		u.assets.ServeHTTP(w, r)
		return
	}
	token := strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer ")
	if strings.EqualFold(r.Header.Get("Upgrade"), "websocket") && token == "" {
		token = r.URL.Query().Get("token")
	}
	if subtle.ConstantTimeCompare([]byte(token), []byte(u.token)) != 1 {
		w.Header().Set("X-AnyUSB-Local-Auth", "required")
		http.Error(w, "run anyusb ui for the authorized URL", http.StatusUnauthorized)
		return
	}
	copy := r.Clone(r.Context())
	copy.Host = core.LocalHost
	copy.Header.Del("Origin")
	copy.Header.Del("Authorization")
	u.API.ServeHTTP(w, copy)
}
