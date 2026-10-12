package api

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"qqbot/internal/config"
	"qqbot/internal/napcat"
	"qqbot/internal/store"
)

func atomicConfigRouter(t *testing.T) *Router {
	t.Helper()
	st, err := store.Open(filepath.Join(t.TempDir(), "config.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = st.Close() })
	m := config.NewManager(filepath.Join(t.TempDir(), config.FileName), st)
	if err := m.Save(config.Config{NewAPI: config.NewAPI{URL: "http://gateway.test", Username: "admin", Password: "secret"}, NapCat: config.NapCat{WebUIURL: "http://qq.test", WebUIToken: "latest-secret"}}); err != nil {
		t.Fatal(err)
	}
	return &Router{st: st, cfg: m}
}

func atomicConfigRequest(handler http.HandlerFunc, in any) *httptest.ResponseRecorder {
	raw, _ := json.Marshal(in)
	w := httptest.NewRecorder()
	handler(w, httptest.NewRequest("POST", "/", strings.NewReader(string(raw))))
	return w
}

type atomicAdapterAdmin struct {
	calls   atomic.Int32
	entered chan string
	release chan struct{}
}

func (a *atomicAdapterAdmin) EnsureAdapters(_ string, token string, _ napcat.AdapterOptions) (*napcat.AdapterResult, error) {
	a.calls.Add(1)
	if a.entered != nil {
		a.entered <- token
		<-a.release
	}
	return &napcat.AdapterResult{OneBotURL: "http://qq.test:3002", EventWSURL: "ws://qq.test:3001", Token: "adapter-secret"}, nil
}

func TestAtomicAutoConfigureRejectsStaleBeforeUpstream(t *testing.T) {
	r := atomicConfigRouter(t)
	a := &atomicAdapterAdmin{}
	r.napcatAdmin = a
	w := atomicConfigRequest(r.autoConfigureNapCat, map[string]any{"revision": "stale", "webui_token": config.RedactedPassword})
	if w.Code != 409 {
		t.Fatalf("code=%d body=%s", w.Code, w.Body.String())
	}
	if a.calls.Load() != 0 {
		t.Fatal("stale request reached upstream")
	}
}

func TestAtomicAutoConfigureConcurrentRevisionOnlyOneUpstream(t *testing.T) {
	r := atomicConfigRouter(t)
	a := &atomicAdapterAdmin{entered: make(chan string, 2), release: make(chan struct{})}
	r.napcatAdmin = a
	rev := r.cfg.SectionRevision(config.SectionNapCat)
	in := map[string]any{"revision": rev, "webui_token": config.RedactedPassword}
	done := make(chan *httptest.ResponseRecorder, 2)
	go func() { done <- atomicConfigRequest(r.autoConfigureNapCat, in) }()
	select {
	case token := <-a.entered:
		if token != "latest-secret" {
			t.Errorf("wrong secret: %q", token)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("upstream not entered")
	}
	go func() { done <- atomicConfigRequest(r.autoConfigureNapCat, in) }()
	select {
	case <-a.entered:
		t.Error("second request penetrated guard")
	case <-time.After(50 * time.Millisecond):
	}
	close(a.release)
	statuses := map[int]int{}
	for range 2 {
		select {
		case w := <-done:
			statuses[w.Code]++
		case <-time.After(2 * time.Second):
			t.Fatal("deadlocked")
		}
	}
	if statuses[200] != 1 || statuses[409] != 1 || a.calls.Load() != 1 {
		t.Fatalf("statuses=%v upstream=%d", statuses, a.calls.Load())
	}
	if got := r.cfg.MustLoad(); got.NapCat.OneBotToken != "adapter-secret" {
		t.Fatalf("not persisted: %+v", got.Redacted())
	}
}

func TestAtomicAPIConfigCASConcurrent(t *testing.T) {
	for _, section := range []string{"", config.SectionNewAPI, config.SectionNapCat} {
		t.Run(section, func(t *testing.T) {
			r := atomicConfigRouter(t)
			handler := http.HandlerFunc(r.saveConfig)
			rev := r.cfg.Revision()
			if section == config.SectionNewAPI {
				handler = r.saveNewAPIConfig
				rev = r.cfg.SectionRevision(section)
			}
			if section == config.SectionNapCat {
				handler = r.saveNapCatConfig
				rev = r.cfg.SectionRevision(section)
			}
			base := r.cfg.MustLoad()
			start := make(chan struct{})
			done := make(chan *httptest.ResponseRecorder, 12)
			for i := range 12 {
				go func(i int) {
					c := base
					c.NewAPI.Username = fmt.Sprint(i)
					c.NapCat.WebUIToken = fmt.Sprint(i)
					<-start
					done <- atomicConfigRequest(handler, map[string]any{"config": c, "revision": rev})
				}(i)
			}
			close(start)
			statuses := map[int]int{}
			for range 12 {
				select {
				case w := <-done:
					statuses[w.Code]++
				case <-time.After(3 * time.Second):
					t.Fatal("deadlocked")
				}
			}
			if statuses[200] != 1 || statuses[409] != 11 {
				t.Fatalf("statuses=%v", statuses)
			}
		})
	}
}
