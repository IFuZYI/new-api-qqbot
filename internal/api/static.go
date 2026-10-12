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
//
// Caching is split by file kind, because getting it wrong is invisible until
// the next deploy:
//
//   - Vite writes the bundle to a content-hashed name (index-<hash>.js), so an
//     asset URL's content can never change: it is safe to cache for a year.
//   - index.html keeps its name across deploys while its <script src> changes,
//     so it must be revalidated. Without an explicit Cache-Control the browser
//     applies heuristic freshness and serves the stale HTML from disk, which
//     points at the previous bundle — the operator sees the old UI after an
//     upgrade and only a manual hard-refresh fixes it.
func staticHandler(dir string) http.Handler {
	root, err := os.OpenRoot(dir)
	if err == nil {
		_, err = root.Stat("index.html")
		_ = root.Close()
	}
	if err != nil && !os.IsNotExist(err) {
		return http.NotFoundHandler()
	}
	if err != nil {
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

	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.HasPrefix(r.URL.Path, "/api/") {
			http.NotFound(w, r)
			return
		}
		// Keep every lookup and open beneath the same root, including when a
		// symlink changes between the metadata check and the file server's open.
		root, err := os.OpenRoot(dir)
		if err != nil {
			http.NotFound(w, r)
			return
		}
		defer root.Close()
		clean := filepath.Clean("/" + r.URL.Path)
		name := strings.TrimPrefix(filepath.ToSlash(clean), "/")
		if name == "" {
			name = "."
		}
		info, err := root.Stat(name)
		if err != nil && !os.IsNotExist(err) {
			http.NotFound(w, r)
			return
		}
		if err == nil && !info.IsDir() {
			w.Header().Set("Cache-Control", assetCacheControl(clean))
			http.FileServerFS(root.FS()).ServeHTTP(w, r)
			return
		}
		// The SPA fallback always serves index.html, so it takes index.html's
		// caching rule whatever the requested path looked like.
		if _, err := root.Stat("index.html"); err != nil {
			http.NotFound(w, r)
			return
		}
		w.Header().Set("Cache-Control", indexCacheControl)
		http.ServeFileFS(w, r, root.FS(), "index.html")
	})
}

const (
	// indexCacheControl must revalidate: the file name is stable across
	// deploys while its contents are not.
	indexCacheControl = "no-cache"

	// hashedAssetCacheControl is for the content-hashed bundle, whose URL
	// changes whenever its contents do.
	hashedAssetCacheControl = "public, max-age=31536000, immutable"
)

// assetCacheControl picks the rule for a requested path. Everything under
// /assets/ is content-hashed by Vite; anything else (a favicon, a manifest, a
// file an operator dropped in) may be edited in place under the same name, so
// it is revalidated rather than cached for a year.
func assetCacheControl(cleanPath string) string {
	if strings.HasPrefix(cleanPath, "/assets/") {
		return hashedAssetCacheControl
	}
	return indexCacheControl
}

// exists reports whether a path is present, used to pick a default static dir.
func exists(path string) bool {
	_, err := fs.Stat(os.DirFS("."), path)
	return err == nil
}
