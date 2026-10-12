package api

import (
	"qqbot/internal/config"
	"testing"
)

func TestConfigResponseRevisionBelongsToSnapshot(t *testing.T) {
	r := atomicConfigRouter(t)
	old := r.cfg.MustLoad()
	first := r.configResponse(old)
	changed := old
	changed.NewAPI.Username = "another-editor"
	if err := r.cfg.Save(changed); err != nil {
		t.Fatal(err)
	}
	stale := r.configResponse(old)
	if stale["revision"] != first["revision"] {
		t.Fatal("old snapshot borrowed latest full revision")
	}
	revs := stale["revisions"].(map[string]string)
	firstRevs := first["revisions"].(map[string]string)
	if revs[config.SectionNewAPI] != firstRevs[config.SectionNewAPI] {
		t.Fatal("old snapshot borrowed latest section revision")
	}
	w := atomicConfigRequest(r.saveNewAPIConfig, map[string]any{"config": old, "revision": revs[config.SectionNewAPI]})
	if w.Code != 409 {
		t.Fatalf("stale snapshot overwrote newer config: HTTP %d", w.Code)
	}
}
