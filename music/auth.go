package music

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"net"
	"net/http"
	"net/url"

	spotify "github.com/zmb3/spotify/v2"
	spotifyauth "github.com/zmb3/spotify/v2/auth"
	"golang.org/x/oauth2"
)

func newAuthenticator(cfg *Config) *spotifyauth.Authenticator {
	return spotifyauth.New(
		spotifyauth.WithClientID(cfg.ClientID),
		spotifyauth.WithClientSecret(cfg.ClientSecret),
		spotifyauth.WithRedirectURL(cfg.RedirectURI),
		spotifyauth.WithScopes(
			spotifyauth.ScopeUserReadPrivate,
			spotifyauth.ScopeUserReadPlaybackState,
			spotifyauth.ScopeUserModifyPlaybackState,
			spotifyauth.ScopeUserReadCurrentlyPlaying,
		),
	)
}

func randomState() string {
	b := make([]byte, 16)
	_, _ = rand.Read(b)
	return hex.EncodeToString(b)
}

// cmdLogin runs the authorization-code flow. It spins up a tiny HTTP server on
// the host/port of the redirect URI, prints the authorization URL, and waits
// for Spotify to redirect back with a code.
func cmdLogin(cfg *Config) error {
	if cfg.ClientID == "" || cfg.ClientSecret == "" {
		return fmt.Errorf("missing credentials: set SPOTIFY_ID and SPOTIFY_SECRET, or run 'spoticli config'")
	}

	auth := newAuthenticator(cfg)
	state := randomState()

	u, err := url.Parse(cfg.RedirectURI)
	if err != nil {
		return fmt.Errorf("invalid redirect URI %q: %w", cfg.RedirectURI, err)
	}
	host := u.Host
	if host == "" {
		host = "localhost:8080"
	}
	path := u.Path
	if path == "" {
		path = "/callback"
	}

	tokCh := make(chan *oauth2.Token, 1)
	errCh := make(chan error, 1)

	mux := http.NewServeMux()
	mux.HandleFunc(path, func(w http.ResponseWriter, r *http.Request) {
		if st := r.FormValue("state"); st != state {
			http.Error(w, "state mismatch", http.StatusBadRequest)
			errCh <- fmt.Errorf("state mismatch in callback")
			return
		}
		tok, err := auth.Token(r.Context(), state, r)
		if err != nil {
			http.Error(w, "could not get token", http.StatusForbidden)
			errCh <- err
			return
		}
		fmt.Fprintln(w, "Login successful. You can close this tab and return to the terminal.")
		tokCh <- tok
	})

	ln, err := net.Listen("tcp", host)
	if err != nil {
		return fmt.Errorf("cannot listen on %s: %w", host, err)
	}
	srv := &http.Server{Handler: mux}
	go srv.Serve(ln) //nolint:errcheck
	defer srv.Close()

	fmt.Println("Open this URL in a browser and authorize the app:")
	fmt.Println()
	fmt.Println("  " + auth.AuthURL(state))
	fmt.Println()
	fmt.Printf("Waiting for the redirect to %s ...\n", cfg.RedirectURI)
	fmt.Println("(Headless? See the SSH port-forward tip in the README.)")

	select {
	case tok := <-tokCh:
		if err := saveToken(tok); err != nil {
			return err
		}
		fmt.Println("Authorized. Token saved.")
		return nil
	case err := <-errCh:
		return err
	}
}

// getClient loads the saved token, builds an authenticated Spotify client whose
// underlying token source auto-refreshes, and returns a save() callback that
// persists a refreshed token back to disk.
func getClient(ctx context.Context, cfg *Config) (*spotify.Client, func(), error) {
	tok, err := loadToken()
	if err != nil {
		return nil, nil, fmt.Errorf("not logged in — run 'spoticli login' first (%w)", err)
	}

	auth := newAuthenticator(cfg)
	httpClient := auth.Client(ctx, tok)
	client := spotify.New(httpClient)

	save := func() {
		nt, err := client.Token()
		if err != nil || nt == nil {
			return
		}
		if nt.AccessToken != tok.AccessToken || !nt.Expiry.Equal(tok.Expiry) {
			_ = saveToken(nt)
		}
	}
	return client, save, nil
}
