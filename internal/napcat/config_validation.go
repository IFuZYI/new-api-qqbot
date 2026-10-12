package napcat

import (
	"encoding/json"
	"fmt"
	"strings"
)

// GetConfig is a schema-loaded response, not a partial patch. Require its
// network object; within it missing lists have upstream's default [] semantics.
// Unknown keys remain opaque so newer NapCat versions can round-trip them.
func validateNetwork(network map[string]json.RawMessage) error {
	for _, list := range []string{"httpServers", "httpSseServers", "httpClients", "websocketServers", "websocketClients", "plugins"} {
		adapters, err := decodeAdapters(network[list])
		if err != nil {
			return fmt.Errorf("OneBot network %s: %w", list, err)
		}
		fields := map[string]string{"name": "string", "enable": "bool", "messagePostFormat": "string", "debug": "bool"}
		if list != "plugins" {
			fields["token"] = "string"
		}
		if strings.HasSuffix(list, "Servers") {
			fields["host"] = "string"
			fields["port"] = "number"
		}
		if strings.HasSuffix(list, "Clients") {
			fields["url"] = "string"
		}
		if list == "httpServers" || list == "httpSseServers" {
			fields["enableCors"] = "bool"
			fields["enableWebsocket"] = "bool"
		}
		if list != "httpServers" {
			fields["reportSelfMessage"] = "bool"
		}
		if strings.HasPrefix(list, "websocket") {
			fields["heartInterval"] = "number"
		}
		if list == "websocketServers" {
			fields["enableForcePushEvent"] = "bool"
		}
		if list == "websocketClients" {
			fields["reconnectInterval"] = "number"
		}
		for i, adapter := range adapters {
			for field, kind := range fields {
				value, exists := adapter[field]
				if !exists {
					continue
				} // Upstream supplies defaults for absent fields.
				valid := false
				switch kind {
				case "string":
					_, valid = value.(string)
				case "bool":
					_, valid = value.(bool)
				case "number":
					_, valid = value.(json.Number)
				}
				if !valid {
					return fmt.Errorf("OneBot network %s[%d].%s: expected %s", list, i, field, kind)
				}
			}
		}
	}
	return nil
}
