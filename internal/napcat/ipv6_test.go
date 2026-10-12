package napcat

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"testing"
)

func TestR6IPv6AdapterURLsAndBind(t *testing.T) {
	for _, tc := range []struct{ bind, connect, wantHost string }{
		{"::1", "", "::1"}, {"::", "", "::1"}, {"::", "2001:db8::42", "2001:db8::42"},
		{"127.0.0.1", WebUIHost("http://localhost:6099"), "127.0.0.1"}, {"0.0.0.0", "", "127.0.0.1"},
	} {
		t.Run(tc.bind+"/"+tc.connect, func(t *testing.T) {
			var sent struct {
				Network map[string][]map[string]any `json:"network"`
			}
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.URL.Path == "/api/OB11Config/GetConfig" {
					fmt.Fprint(w, `{"code":0,"data":{"network":{}}}`)
					return
				}
				var body map[string]string
				json.NewDecoder(r.Body).Decode(&body)
				json.Unmarshal([]byte(body["config"]), &sent)
				fmt.Fprint(w, `{"code":0,"data":null}`)
			}))
			defer srv.Close()
			result, err := NewWebUI(srv.URL, "secret").EnsureAdapters(AdapterOptions{BindHost: tc.bind, ConnectHost: tc.connect})
			if err != nil {
				t.Fatal(err)
			}
			for _, entry := range []struct{ raw, port, scheme string }{{result.OneBotURL, "3002", "http"}, {result.EventWSURL, "3001", "ws"}} {
				u, err := url.Parse(entry.raw)
				if err != nil || u.Hostname() != tc.wantHost || u.Port() != entry.port || u.Scheme != entry.scheme {
					t.Errorf("malformed URL %s, want host %s port %s (err=%v)", entry.raw, tc.wantHost, entry.port, err)
				}
				if err == nil && tc.wantHost == "::1" && u.Host != "[::1]:"+entry.port {
					t.Errorf("IPv6 host not bracketed: %s", entry.raw)
				}
			}
			for _, list := range []string{"httpServers", "websocketServers"} {
				if sent.Network[list][0]["host"] != tc.bind {
					t.Errorf("bind changed: %v", sent.Network[list])
				}
			}
		})
	}
}
