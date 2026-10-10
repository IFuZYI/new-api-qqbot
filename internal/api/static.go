package api

import (
	"io/fs"
	"net/http"
	"os"
	"path/filepath"
	"strings"
)

// staticHandler serves the built WebUI from dir, falling back to index.html for
// client-side routes. When dir does not exist (a backend-only checkout) it
// returns a short explanatory page instead of a bare 404, so the API stays
// usable before the frontend has been built.
func staticHandler(dir string) http.Handler {
	index := filepath.Join(dir, "index.html")
	if _, err := os.Stat(index); err != nil {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if strings.HasPrefix(r.URL.Path, "/api/") {
				http.NotFound(w, r)
				return
			}
			w.Header().Set("Content-Type", "text/html; charset=utf-8")
			_, _ = w.Write([]byte(`<!doctype html><meta charset="utf-8">` +
				`<title>QQBot</title><body style="font-family:system-ui;background:#0f1115;color:#e6e8ee;padding:2rem">` +
				`<h1>QQBot</h1><p>前端尚未构建。在 <code>web/</code> 下执行 <code>npm install &amp;&amp; npm run build</code>，` +
				`或把构建产物放到 <code>` + dir + `</code>。</p>` +
				`<p>API 可正常访问：<code>/api/bootstrap</code></p>`))
		})
	}

	fileServer := http.FileServer(http.Dir(dir))
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.HasPrefix(r.URL.Path, "/api/") {
			http.NotFound(w, r)
			return
		}
		clean := filepath.Clean("/" + r.URL.Path)
		full := filepath.Join(dir, filepath.FromSlash(clean))
		if info, err := os.Stat(full); err == nil && !info.IsDir() {
			fileServer.ServeHTTP(w, r)
			return
		}
		http.ServeFile(w, r, index)
	})
}

// exists reports whether a path is present, used to pick a default static dir.
func exists(path string) bool {
	_, err := fs.Stat(os.DirFS("."), path)
	return err == nil
}
