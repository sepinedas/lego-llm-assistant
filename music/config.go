package music

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"

	"golang.org/x/oauth2"
)

// Config holds the user's Spotify app credentials and preferences.
// It lives at $XDG_CONFIG_HOME/spoticli/config.json (usually ~/.config/spoticli).
type Config struct {
	ClientID     string `json:"client_id"`
	ClientSecret string `json:"client_secret"`
	RedirectURI  string `json:"redirect_uri"`
	DeviceName   string `json:"device_name"` // preferred Spotify Connect device, e.g. "raspotify"
}

const (
	// Spotify no longer permits "localhost" in redirect URIs; a loopback IP
	// literal is required (http is still allowed for loopback addresses).
	defaultRedirect = "http://127.0.0.1:8080/callback"
	defaultDevice   = "raspotify"
)

func configDir() (string, error) {
	dir, err := os.UserConfigDir()
	if err != nil {
		return "", err
	}
	d := filepath.Join(dir, "spoticli")
	return d, os.MkdirAll(d, 0o700)
}

func configPath() (string, error) {
	d, err := configDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(d, "config.json"), nil
}

func tokenPath() (string, error) {
	d, err := configDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(d, "token.json"), nil
}

// loadConfig reads config.json (if present) and lets environment variables
// override the file. Sensible defaults are filled in for missing fields.
func loadConfig() (*Config, error) {
	cfg := &Config{
		RedirectURI: defaultRedirect,
		DeviceName:  defaultDevice,
	}

	p, err := configPath()
	if err != nil {
		return nil, err
	}
	b, err := os.ReadFile(p)
	switch {
	case errors.Is(err, os.ErrNotExist):
		// No file yet; rely on env vars / defaults.
	case err != nil:
		return nil, err
	default:
		if err := json.Unmarshal(b, cfg); err != nil {
			return nil, err
		}
	}

	if v := os.Getenv("SPOTIFY_ID"); v != "" {
		cfg.ClientID = v
	}
	if v := os.Getenv("SPOTIFY_SECRET"); v != "" {
		cfg.ClientSecret = v
	}
	if v := os.Getenv("SPOTIFY_REDIRECT_URI"); v != "" {
		cfg.RedirectURI = v
	}
	if v := os.Getenv("SPOTICLI_DEVICE"); v != "" {
		cfg.DeviceName = v
	}
	if cfg.RedirectURI == "" {
		cfg.RedirectURI = defaultRedirect
	}
	if cfg.DeviceName == "" {
		cfg.DeviceName = defaultDevice
	}
	return cfg, nil
}

func saveConfig(cfg *Config) error {
	p, err := configPath()
	if err != nil {
		return err
	}
	b, err := json.MarshalIndent(cfg, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(p, b, 0o600)
}

func loadToken() (*oauth2.Token, error) {
	p, err := tokenPath()
	if err != nil {
		return nil, err
	}
	b, err := os.ReadFile(p)
	if err != nil {
		return nil, err
	}
	var tok oauth2.Token
	if err := json.Unmarshal(b, &tok); err != nil {
		return nil, err
	}
	return &tok, nil
}

func saveToken(tok *oauth2.Token) error {
	p, err := tokenPath()
	if err != nil {
		return err
	}
	b, err := json.MarshalIndent(tok, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(p, b, 0o600)
}
