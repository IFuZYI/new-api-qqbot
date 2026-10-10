// Package config holds the operator-supplied connection settings for the two
// external systems. Both are editable from the WebUI, so they live in the
// database rather than in flags or environment variables.
package config

import (
	"errors"
	"fmt"
	"net/url"
	"strings"

	"qqbot/internal/store"
)

const (
	newAPIKey = "config.newapi"
	napCatKey = "config.napcat"
)

// NewAPI is the gateway admin account this service drives.
type NewAPI struct {
	URL      string `json:"url"`
	Username string `json:"username"`
	Password string `json:"password"`
}

// NapCat describes how to reach the local NapCat instance. Two transports are
// configured independently: the WebUI (used for reading the OneBot config and
// as a fallback action bridge) and the OneBot HTTP endpoint (used for routine
// action calls).
type NapCat struct {
	WebUIURL   string `json:"webui_url"`
	WebUIToken string `json:"webui_token"`

	OneBotURL   string `json:"onebot_url"`
	OneBotToken string `json:"onebot_token"`

	// EventWSURL receives push events (used for the binding command). Optional:
	// without it the service still works, only with slower reaction.
	EventWSURL   string `json:"event_ws_url"`
	EventWSToken string `json:"event_ws_token"`
}

// Config is the full external-systems configuration.
type Config struct {
	NewAPI NewAPI `json:"newapi"`
	NapCat NapCat `json:"napcat"`
}

// Load reads the configuration. Missing values are not an error: a fresh
// install simply has nothing configured yet.
func Load(st *store.Store) (Config, error) {
	var c Config
	if err := st.GetSetting(newAPIKey, &c.NewAPI); err != nil {
		return c, err
	}
	if err := st.GetSetting(napCatKey, &c.NapCat); err != nil {
		return c, err
	}
	return c, nil
}

// Save stores the configuration.
func Save(st *store.Store, c Config) error {
	if err := st.PutSetting(newAPIKey, c.NewAPI); err != nil {
		return err
	}
	return st.PutSetting(napCatKey, c.NapCat)
}

// ValidateNewAPI checks the gateway settings.
func (n NewAPI) Validate() error {
	if strings.TrimSpace(n.URL) == "" {
		return errors.New("new-api URL is required")
	}
	if err := validateURL(n.URL, "http", "https"); err != nil {
		return fmt.Errorf("new-api URL: %w", err)
	}
	if strings.TrimSpace(n.Username) == "" {
		return errors.New("new-api username is required")
	}
	if n.Password == "" {
		return errors.New("new-api password is required")
	}
	return nil
}

// Validate checks the NapCat settings. At least one transport must be
// configured, otherwise the service has no way to reach QQ.
func (n NapCat) Validate() error {
	if strings.TrimSpace(n.WebUIURL) == "" && strings.TrimSpace(n.OneBotURL) == "" {
		return errors.New("configure either the NapCat WebUI URL or the OneBot URL")
	}
	if n.WebUIURL != "" {
		if err := validateURL(n.WebUIURL, "http", "https"); err != nil {
			return fmt.Errorf("NapCat WebUI URL: %w", err)
		}
		if n.WebUIToken == "" {
			return errors.New("NapCat WebUI token is required when the WebUI URL is set")
		}
	}
	if n.OneBotURL != "" {
		if err := validateURL(n.OneBotURL, "http", "https"); err != nil {
			return fmt.Errorf("OneBot URL: %w", err)
		}
	}
	if n.EventWSURL != "" {
		if err := validateURL(n.EventWSURL, "ws", "wss"); err != nil {
			return fmt.Errorf("event WebSocket URL: %w", err)
		}
	}
	return nil
}

func validateURL(raw string, schemes ...string) error {
	u, err := url.Parse(strings.TrimSpace(raw))
	if err != nil {
		return err
	}
	for _, s := range schemes {
		if u.Scheme == s {
			if u.Host == "" {
				return errors.New("missing host")
			}
			return nil
		}
	}
	return fmt.Errorf("scheme must be one of %s", strings.Join(schemes, ", "))
}

// Redacted returns a copy with secrets replaced, for display in the WebUI.
func (c Config) Redacted() Config {
	out := c
	if out.NewAPI.Password != "" {
		out.NewAPI.Password = "********"
	}
	if out.NapCat.WebUIToken != "" {
		out.NapCat.WebUIToken = "********"
	}
	if out.NapCat.OneBotToken != "" {
		out.NapCat.OneBotToken = "********"
	}
	if out.NapCat.EventWSToken != "" {
		out.NapCat.EventWSToken = "********"
	}
	return out
}

// RedactedPassword is the placeholder the WebUI sends back unchanged when the
// operator did not edit the field.
const RedactedPassword = "********"

// Merge keeps stored secrets when the incoming value is the redaction
// placeholder, so saving the form without retyping a password does not wipe it.
func (c Config) Merge(stored Config) Config {
	out := c
	if out.NewAPI.Password == RedactedPassword || out.NewAPI.Password == "" {
		out.NewAPI.Password = stored.NewAPI.Password
	}
	if out.NapCat.WebUIToken == RedactedPassword || out.NapCat.WebUIToken == "" {
		out.NapCat.WebUIToken = stored.NapCat.WebUIToken
	}
	if out.NapCat.OneBotToken == RedactedPassword || out.NapCat.OneBotToken == "" {
		out.NapCat.OneBotToken = stored.NapCat.OneBotToken
	}
	if out.NapCat.EventWSToken == RedactedPassword || out.NapCat.EventWSToken == "" {
		out.NapCat.EventWSToken = stored.NapCat.EventWSToken
	}
	return out
}
