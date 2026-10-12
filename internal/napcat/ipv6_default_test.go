package napcat

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestIPv6DefaultAdaptersUseMatchingLoopback(t *testing.T) {
	var saved struct {
		Network map[string][]map[string]any `json:"network"`
	}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/api/OB11Config/GetConfig" {
			fmt.Fprint(w, `{"code":0,"data":{"network":{}}}`)
			return
		}
		var body map[string]string
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Error(err)
		}
		if err := json.Unmarshal([]byte(body["config"]), &saved); err != nil {
			t.Error(err)
		}
		fmt.Fprint(w, `{"code":0,"data":null}`)
	}))
	defer srv.Close()
	result, err := NewWebUI(srv.URL, "fixture").EnsureAdapters(AdapterOptions{ConnectHost: WebUIHost("http://[::1]:6099")})
	if err != nil {
		t.Fatal(err)
	}
	for _, list := range []string{"httpServers", "websocketServers"} {
		if len(saved.Network[list]) != 1 {
			t.Fatalf("missing adapter %s", list)
		}
		if host := saved.Network[list][0]["host"]; host != "::1" {
			t.Errorf("%s binds %v, want IPv6 loopback", list, host)
		}
	}
	if result.OneBotURL != "http://[::1]:3002" || result.EventWSURL != "ws://[::1]:3001" {
		t.Fatalf("bad URLs: %+v", result)
	}
}
