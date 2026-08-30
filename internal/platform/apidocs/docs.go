// Package apidocs serves pinned local Swagger assets with no runtime CDN dependency.
package apidocs

import (
	"embed"
	"github.com/F1reStyLe/AlUrMessenger/api/openapi"
	"github.com/F1reStyLe/AlUrMessenger/internal/platform/httpserver"
	"net/http"
	"strconv"
)

// assets contains only reviewed distribution files; requests cannot list this FS.
//
//go:embed assets/*
var assets embed.FS

// Handler is a small explicit route allowlist. No file server/path traversal or index listing.
func Handler() http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != "GET" && r.Method != "HEAD" {
			w.Header().Set("Allow", "GET, HEAD")
			httpserver.WriteError(w, r, 405, "METHOD_NOT_ALLOWED", "Method not allowed")
			return
		}
		// Script is same-origin only. Swagger uses inline styles; no inline script/eval needed.
		w.Header().Set("Content-Security-Policy", "default-src 'none'; script-src 'self'; style-src 'self' 'unsafe-inline'; img-src 'self' data:; font-src 'self'; connect-src 'self'; base-uri 'none'; frame-ancestors 'none'; form-action 'self'")
		w.Header().Set("Referrer-Policy", "no-referrer")
		var data []byte
		var contentType string
		var err error
		switch r.URL.Path {
		case "/docs/api", "/docs/api/":
			data, err = assets.ReadFile("assets/index.html")
			contentType = "text/html; charset=utf-8"
		case "/docs/api/openapi.json":
			data = openapi.Document
			contentType = "application/json; charset=utf-8"
		case "/docs/api/swagger-ui-bundle.js", "/docs/api/initializer.js":
			data, err = assets.ReadFile("assets/" + r.URL.Path[len("/docs/api/"):])
			contentType = "text/javascript; charset=utf-8"
		case "/docs/api/swagger-ui.css":
			data, err = assets.ReadFile("assets/swagger-ui.css")
			contentType = "text/css; charset=utf-8"
		default:
			httpserver.WriteError(w, r, 404, "RESOURCE_NOT_FOUND", "Documentation asset not found")
			return
		}
		if err != nil {
			httpserver.WriteError(w, r, 500, "INTERNAL_ERROR", "Documentation unavailable")
			return
		}
		w.Header().Set("Content-Type", contentType)
		w.Header().Set("Content-Length", strconv.Itoa(len(data)))
		w.WriteHeader(200)
		if r.Method != "HEAD" {
			_, _ = w.Write(data)
		}
	})
}

// Register exposes docs on API only. Swagger never adds routes to the worker process.
func Register(server *httpserver.Server) {
	handler := Handler()
	server.Handle("/docs/api", handler)
	server.Handle("/docs/api/", handler)
}
