package api

import (
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func staticBoundaryWrite(t *testing.T, name, body string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(name), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(name, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
}

func staticBoundaryLink(t *testing.T, target, name string) {
	t.Helper()
	if err := os.Symlink(target, name); err != nil {
		t.Fatal(err)
	}
}

func TestStaticBoundaryIndexFallback(t *testing.T) {
	for _, replaceAfterInit := range []bool{false, true} {
		name := "existing-symlink"
		if replaceAfterInit {
			name = "replaced-after-init"
		}
		t.Run(name, func(t *testing.T) {
			base := t.TempDir()
			dir := filepath.Join(base, "web")
			index := filepath.Join(dir, "index.html")
			staticBoundaryWrite(t, index, "<html>INSIDE_INDEX</html>")
			staticBoundaryWrite(t, filepath.Join(base, "secret.html"), "OUTSIDE_INDEX_FIXTURE")
			var h http.Handler
			if replaceAfterInit {
				h = staticHandler(dir)
			}
			if err := os.Remove(index); err != nil {
				t.Fatal(err)
			}
			staticBoundaryLink(t, "../secret.html", index)
			if h == nil {
				h = staticHandler(dir)
			}
			for _, path := range []string{"/", "/users/42", "/assets/missing.js"} {
				w := httptest.NewRecorder()
				h.ServeHTTP(w, httptest.NewRequest(http.MethodGet, path, nil))
				if w.Code != http.StatusNotFound || strings.Contains(w.Body.String(), "OUTSIDE_INDEX_FIXTURE") {
					t.Errorf("GET %s: status=%d body=%q; want 404 and no outside index", path, w.Code, w.Body.String())
				}
			}
		})
	}
}

func TestStaticBoundaryRegression(t *testing.T) {
	dir := t.TempDir()
	staticBoundaryWrite(t, filepath.Join(dir, "shell.html"), "<html>INSIDE_INDEX</html>")
	staticBoundaryLink(t, "shell.html", filepath.Join(dir, "index.html"))
	staticBoundaryWrite(t, filepath.Join(dir, "assets", "app-hash.js"), "INSIDE_ASSET")
	staticBoundaryWrite(t, filepath.Join(dir, "favicon.ico"), "ICON")
	h := staticHandler(dir)
	for _, tc := range []struct {
		path  string
		code  int
		body  string
		cache string
	}{
		{"/", http.StatusOK, "<html>INSIDE_INDEX</html>", indexCacheControl},
		{"/users/42", http.StatusOK, "<html>INSIDE_INDEX</html>", indexCacheControl},
		{"/assets/missing.js", http.StatusOK, "<html>INSIDE_INDEX</html>", indexCacheControl},
		{"/assets/app-hash.js", http.StatusOK, "INSIDE_ASSET", hashedAssetCacheControl},
		{"/favicon.ico", http.StatusOK, "ICON", indexCacheControl},
		{"/api/missing", http.StatusNotFound, "404 page not found\n", ""},
		{"/../missing", http.StatusBadRequest, "invalid URL path\n", ""},
		{"/%2e%2e/missing", http.StatusBadRequest, "invalid URL path\n", ""},
	} {
		t.Run(tc.path, func(t *testing.T) {
			w := httptest.NewRecorder()
			h.ServeHTTP(w, httptest.NewRequest(http.MethodGet, tc.path, nil))
			if w.Code != tc.code || w.Body.String() != tc.body || w.Header().Get("Cache-Control") != tc.cache {
				t.Fatalf("GET %s: status=%d body=%q cache=%q; want status=%d body=%q cache=%q", tc.path, w.Code, w.Body.String(), w.Header().Get("Cache-Control"), tc.code, tc.body, tc.cache)
			}
		})
	}
	if err := os.Remove(filepath.Join(dir, "index.html")); err != nil {
		t.Fatal(err)
	}
	w := httptest.NewRecorder()
	h.ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/users/42", nil))
	if w.Code != http.StatusNotFound {
		t.Fatalf("missing index after init: status=%d, want 404", w.Code)
	}
}

func TestStaticBoundaryMissingBuild(t *testing.T) {
	base := t.TempDir()
	for _, dir := range []string{base, filepath.Join(base, "missing-dist")} {
		h := staticHandler(dir)
		for _, path := range []string{"/", "/users/42", "/api/missing"} {
			w := httptest.NewRecorder()
			h.ServeHTTP(w, httptest.NewRequest(http.MethodGet, path, nil))
			if strings.HasPrefix(path, "/api/") {
				if w.Code != http.StatusNotFound {
					t.Errorf("missing build API status=%d, want 404", w.Code)
				}
				continue
			}
			if w.Code != http.StatusOK || !strings.Contains(w.Body.String(), "前端尚未构建") || !strings.Contains(w.Header().Get("Content-Type"), "text/html") {
				t.Errorf("missing build %s: status=%d body=%q", path, w.Code, w.Body.String())
			}
		}
	}
}

func TestStaticBoundarySymlinks(t *testing.T) {
	base := t.TempDir()
	dir := filepath.Join(base, "web")
	staticBoundaryWrite(t, filepath.Join(dir, "index.html"), "<html>INSIDE_INDEX</html>")
	staticBoundaryWrite(t, filepath.Join(dir, "assets", "app-hash.js"), "INSIDE_ASSET")
	staticBoundaryWrite(t, filepath.Join(base, "outside", "secret.txt"), "OUTSIDE_FIXTURE")
	staticBoundaryLink(t, "assets/app-hash.js", filepath.Join(dir, "inside.js"))
	staticBoundaryLink(t, "assets", filepath.Join(dir, "inside-dir"))
	staticBoundaryLink(t, "../outside/secret.txt", filepath.Join(dir, "leak.txt"))
	staticBoundaryLink(t, "../outside", filepath.Join(dir, "leak-dir"))
	staticBoundaryLink(t, filepath.Join(base, "outside", "secret.txt"), filepath.Join(dir, "absolute.txt"))
	h := staticHandler(dir)
	for _, tc := range []struct {
		path string
		code int
		body string
	}{
		{"/inside.js", http.StatusOK, "INSIDE_ASSET"},
		{"/inside-dir/app-hash.js", http.StatusOK, "INSIDE_ASSET"},
		{"/leak.txt", http.StatusNotFound, ""},
		{"/leak-dir/secret.txt", http.StatusNotFound, ""},
		{"/absolute.txt", http.StatusNotFound, ""},
	} {
		t.Run(tc.path, func(t *testing.T) {
			w := httptest.NewRecorder()
			h.ServeHTTP(w, httptest.NewRequest(http.MethodGet, tc.path, nil))
			if w.Code != tc.code || (tc.body != "" && w.Body.String() != tc.body) || strings.Contains(w.Body.String(), "OUTSIDE_FIXTURE") {
				t.Fatalf("GET %s: status=%d body=%q; want status=%d body=%q and no outside data", tc.path, w.Code, w.Body.String(), tc.code, tc.body)
			}
		})
	}
}
