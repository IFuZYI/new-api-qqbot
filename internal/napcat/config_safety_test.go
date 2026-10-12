package napcat

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"reflect"
	"sync/atomic"
	"testing"
)

func TestR6MalformedConfigNeverWrites(t *testing.T) {
	configs := []string{`{}`, `{"network":null}`, `{"network":[]}`, `{"network":"bad"}`, `{"network":true}`}
	for _, list := range []string{"httpServers", "httpSseServers", "httpClients", "websocketServers", "websocketClients", "plugins"} {
		for _, bad := range []string{`null`, `{}`, `"bad"`, `[null]`, `[1]`, `[{"name":12}]`, `[{"enable":"yes"}]`, `[{"debug":null}]`} {
			configs = append(configs, fmt.Sprintf(`{"network":{%q:%s}}`, list, bad))
		}
	}
	for _, raw := range configs {
		t.Run(raw, func(t *testing.T) {
			var writes atomic.Int32
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.URL.Path == "/api/OB11Config/GetConfig" {
					fmt.Fprintf(w, `{"code":0,"data":%s}`, raw)
				} else {
					writes.Add(1)
					fmt.Fprint(w, `{"code":0,"data":null}`)
				}
			}))
			defer srv.Close()
			c := NewWebUI(srv.URL, "secret")
			if cfg, err := c.GetOneBotConfig(); err == nil || cfg != nil {
				t.Errorf("GetConfig accepted malformed config: %+v, %v", cfg, err)
			}
			if result, err := c.EnsureAdapters(AdapterOptions{}); err == nil || result != nil {
				t.Errorf("EnsureAdapters accepted malformed config: %+v, %v", result, err)
			}
			if writes.Load() != 0 {
				t.Fatalf("destructive writes=%d", writes.Load())
			}
		})
	}
}

func TestR6ConfigPreservesUnknownAndUserAdapters(t *testing.T) {
	for _, mode := range []string{"ensure", "http", "ws"} {
		t.Run(mode, func(t *testing.T) {
			const raw = `{"future":{"large":9007199254740993},"network":{"futureNetwork":{"v":42},"httpServers":[{"name":"operator-http","future":9007199254740993},{"name":"qqbot-http","future":"keep-http"}],"websocketServers":[{"name":"operator-ws","future":9007199254740993},{"name":"qqbot-ws","future":"keep-ws"}],"plugins":[{"name":"operator-plugin","future":{"v":1}}]}}`
			var saved map[string]json.RawMessage
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.URL.Path == "/api/OB11Config/GetConfig" {
					fmt.Fprintf(w, `{"code":0,"data":%s}`, raw)
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
			c := NewWebUI(srv.URL, "secret")
			var err error
			switch mode {
			case "ensure":
				_, err = c.EnsureAdapters(AdapterOptions{})
			case "http":
				err = c.EnableHTTPServer(HTTPAdapterName, "127.0.0.1", 3002, "token")
			case "ws":
				err = c.EnableWebSocketServer(WSAdapterName, "127.0.0.1", 3001, "token")
			}
			if err != nil {
				t.Fatal(err)
			}
			var before map[string]json.RawMessage
			json.Unmarshal([]byte(raw), &before)
			if string(saved["future"]) != string(before["future"]) {
				t.Errorf("top-level extension changed: %s", saved["future"])
			}
			var network map[string]json.RawMessage
			json.Unmarshal(saved["network"], &network)
			if string(network["futureNetwork"]) != `{"v":42}` || string(network["plugins"]) != `[{"name":"operator-plugin","future":{"v":1}}]` {
				t.Errorf("network extensions changed: %s", saved["network"])
			}
			for _, list := range []string{"httpServers", "websocketServers"} {
				var adapters []map[string]json.RawMessage
				json.Unmarshal(network[list], &adapters)
				if len(adapters) != 2 {
					t.Errorf("user adapter lost in %s: %s", list, network[list])
					continue
				}
				if string(adapters[0]["future"]) != "9007199254740993" || len(adapters[1]["future"]) == 0 {
					t.Errorf("adapter extensions lost/rounded: %s", network[list])
				}
			}
		})
	}
}

func TestR6EmptyNetworkIsValid(t *testing.T) {
	var saved map[string]json.RawMessage
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/api/OB11Config/GetConfig" {
			fmt.Fprint(w, `{"code":0,"data":{"network":{}}}`)
			return
		}
		var body map[string]string
		json.NewDecoder(r.Body).Decode(&body)
		json.Unmarshal([]byte(body["config"]), &saved)
		fmt.Fprint(w, `{"code":0,"data":null}`)
	}))
	defer srv.Close()
	result, err := NewWebUI(srv.URL, "secret").EnsureAdapters(AdapterOptions{})
	if err != nil || result == nil || reflect.DeepEqual(saved, nil) {
		t.Fatalf("empty object network rejected: result=%v saved=%v err=%v", result, saved, err)
	}
}
