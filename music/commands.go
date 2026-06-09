package music

import (
	"bufio"
	"context"
	"fmt"
	"os"
	"strconv"
	"strings"
	"time"

	spotify "github.com/zmb3/spotify/v2"
)

// resolveDevice finds a Connect device by exact name (case-insensitive) or by ID.
// It does NOT fall back to the active device — callers that want a fallback use
// deviceOpts instead.
func resolveDevice(ctx context.Context, client *spotify.Client, name string) (*spotify.PlayerDevice, error) {
	devs, err := client.PlayerDevices(ctx)
	if err != nil {
		return nil, err
	}
	if len(devs) == 0 {
		return nil, fmt.Errorf("no Spotify Connect devices found — is raspotify running and signed in?")
	}
	for i := range devs {
		d := devs[i]
		if strings.EqualFold(d.Name, name) || string(d.ID) == name {
			return &d, nil
		}
	}
	return nil, fmt.Errorf("device %q not found — run 'spoticli devices' to list them", name)
}

// deviceOpts returns PlayOptions targeting the preferred device, or nil if it
// can't be found (in which case Spotify acts on the currently active device).
func deviceOpts(ctx context.Context, client *spotify.Client, cfg *Config) *spotify.PlayOptions {
	d, err := resolveDevice(ctx, client, cfg.DeviceName)
	if err != nil || d == nil {
		return nil
	}
	id := d.ID
	return &spotify.PlayOptions{DeviceID: &id}
}

func firstTrack(ctx context.Context, client *spotify.Client, q string) (*spotify.FullTrack, error) {
	res, err := client.Search(ctx, q, spotify.SearchTypeTrack, spotify.Limit(1))
	if err != nil {
		return nil, err
	}
	if res.Tracks == nil || len(res.Tracks.Tracks) == 0 {
		return nil, fmt.Errorf("no tracks found for %q", q)
	}
	return &res.Tracks.Tracks[0], nil
}

func artistNames(as []spotify.SimpleArtist) string {
	names := make([]string, len(as))
	for i, a := range as {
		names[i] = a.Name
	}
	return strings.Join(names, ", ")
}

func fmtMs(ms int) string {
	d := time.Duration(ms) * time.Millisecond
	return fmt.Sprintf("%d:%02d", int(d.Minutes()), int(d.Seconds())%60)
}

// ---- commands -------------------------------------------------------------

func cmdDevices(ctx context.Context, client *spotify.Client) error {
	devs, err := client.PlayerDevices(ctx)
	if err != nil {
		return err
	}
	if len(devs) == 0 {
		fmt.Println("No devices found. Is raspotify running and signed in?")
		return nil
	}
	for _, d := range devs {
		mark := " "
		if d.Active {
			mark = "*"
		}
		fmt.Printf("%s %-24s %-10s vol:%3d%%  %s\n", mark, d.Name, d.Type, int(d.Volume), d.ID)
	}
	return nil
}

func cmdUse(ctx context.Context, client *spotify.Client, cfg *Config, args []string) error {
	if len(args) == 0 {
		return fmt.Errorf("usage: spoticli use <device-name|id>")
	}
	name := strings.Join(args, " ")
	d, err := resolveDevice(ctx, client, name)
	if err != nil {
		return err
	}
	if err := client.TransferPlayback(ctx, d.ID, false); err != nil {
		return err
	}
	cfg.DeviceName = d.Name
	if err := saveConfig(cfg); err != nil {
		return err
	}
	fmt.Printf("Now using %q for playback.\n", d.Name)
	return nil
}

func cmdPlay(ctx context.Context, client *spotify.Client, cfg *Config, args []string) error {
	opt := deviceOpts(ctx, client, cfg)
	if len(args) > 0 {
		q := strings.Join(args, " ")
		t, err := firstTrack(ctx, client, q)
		if err != nil {
			return err
		}
		if opt == nil {
			opt = &spotify.PlayOptions{}
		}
		opt.URIs = []spotify.URI{t.URI}
		if err := client.PlayOpt(ctx, opt); err != nil {
			return err
		}
		fmt.Printf("\u25B6 %s \u2014 %s\n", t.Name, artistNames(t.Artists))
		return nil
	}
	if opt == nil {
		if err := client.Play(ctx); err != nil {
			return err
		}
	} else if err := client.PlayOpt(ctx, opt); err != nil {
		return err
	}
	fmt.Println("\u25B6 resumed")
	return nil
}

func cmdPause(ctx context.Context, client *spotify.Client, cfg *Config) error {
	opt := deviceOpts(ctx, client, cfg)
	if opt == nil {
		return client.Pause(ctx)
	}
	return client.PauseOpt(ctx, opt)
}

func cmdNext(ctx context.Context, client *spotify.Client, cfg *Config) error {
	opt := deviceOpts(ctx, client, cfg)
	if opt == nil {
		return client.Next(ctx)
	}
	return client.NextOpt(ctx, opt)
}

func cmdPrev(ctx context.Context, client *spotify.Client, cfg *Config) error {
	opt := deviceOpts(ctx, client, cfg)
	if opt == nil {
		return client.Previous(ctx)
	}
	return client.PreviousOpt(ctx, opt)
}

func cmdSearch(ctx context.Context, client *spotify.Client, args []string) error {
	if len(args) == 0 {
		return fmt.Errorf("usage: spoticli search <query>")
	}
	q := strings.Join(args, " ")
	res, err := client.Search(ctx, q, spotify.SearchTypeTrack, spotify.Limit(10))
	if err != nil {
		return err
	}
	if res.Tracks == nil || len(res.Tracks.Tracks) == 0 {
		fmt.Println("No results.")
		return nil
	}
	for i, t := range res.Tracks.Tracks {
		fmt.Printf("%2d. %-40s %s\n", i+1, truncate(t.Name, 40), artistNames(t.Artists))
	}
	return nil
}

func cmdQueue(ctx context.Context, client *spotify.Client, cfg *Config, args []string) error {
	if len(args) == 0 {
		return fmt.Errorf("usage: spoticli queue <query>")
	}
	t, err := firstTrack(ctx, client, strings.Join(args, " "))
	if err != nil {
		return err
	}
	if err := client.QueueSong(ctx, t.ID); err != nil {
		return err
	}
	fmt.Printf("queued: %s \u2014 %s\n", t.Name, artistNames(t.Artists))
	return nil
}

func cmdVolume(ctx context.Context, client *spotify.Client, cfg *Config, args []string) error {
	if len(args) == 0 {
		return fmt.Errorf("usage: spoticli volume <0-100>")
	}
	v, err := strconv.Atoi(args[0])
	if err != nil || v < 0 || v > 100 {
		return fmt.Errorf("volume must be an integer 0-100")
	}
	opt := deviceOpts(ctx, client, cfg)
	if opt == nil {
		return client.Volume(ctx, v)
	}
	return client.VolumeOpt(ctx, v, opt)
}

func cmdNow(ctx context.Context, client *spotify.Client) error {
	st, err := client.PlayerState(ctx)
	if err != nil {
		return err
	}
	if st.Item == nil {
		fmt.Println("Nothing playing.")
		return nil
	}
	status := "\u25B6 playing"
	if !st.Playing {
		status = "\u23F8 paused"
	}
	fmt.Printf("%s  %s \u2014 %s\n", status, st.Item.Name, artistNames(st.Item.Artists))
	fmt.Printf("   album:  %s\n", st.Item.Album.Name)
	fmt.Printf("   time:   %s / %s\n", fmtMs(int(st.Progress)), fmtMs(int(st.Item.Duration)))
	fmt.Printf("   device: %s   shuffle:%v  repeat:%s\n", st.Device.Name, st.ShuffleState, st.RepeatState)
	return nil
}

func cmdShuffle(ctx context.Context, client *spotify.Client, cfg *Config, args []string) error {
	on := true
	if len(args) > 0 {
		switch strings.ToLower(args[0]) {
		case "off", "false", "0", "no":
			on = false
		}
	}
	opt := deviceOpts(ctx, client, cfg)
	if opt == nil {
		return client.Shuffle(ctx, on)
	}
	return client.ShuffleOpt(ctx, on, opt)
}

func cmdRepeat(ctx context.Context, client *spotify.Client, cfg *Config, args []string) error {
	state := "off"
	if len(args) > 0 {
		state = strings.ToLower(args[0]) // off | track | context
	}
	switch state {
	case "off", "track", "context":
	default:
		return fmt.Errorf("repeat must be one of: off, track, context")
	}
	opt := deviceOpts(ctx, client, cfg)
	if opt == nil {
		return client.Repeat(ctx, state)
	}
	return client.RepeatOpt(ctx, state, opt)
}

// cmdConfig is a small interactive wizard that writes config.json.
func cmdConfig(cfg *Config) error {
	reader := bufio.NewReader(os.Stdin)
	ask := func(label, cur string) string {
		if cur != "" {
			fmt.Printf("%s [%s]: ", label, cur)
		} else {
			fmt.Printf("%s: ", label)
		}
		line, _ := reader.ReadString('\n')
		line = strings.TrimSpace(line)
		if line == "" {
			return cur
		}
		return line
	}
	cfg.ClientID = ask("Spotify Client ID", cfg.ClientID)
	cfg.ClientSecret = ask("Spotify Client Secret", cfg.ClientSecret)
	cfg.RedirectURI = ask("Redirect URI", cfg.RedirectURI)
	cfg.DeviceName = ask("Preferred device name", cfg.DeviceName)
	if err := saveConfig(cfg); err != nil {
		return err
	}
	p, _ := configPath()
	fmt.Printf("Saved %s\n", p)
	return nil
}

// cmdRepl is an interactive prompt that reuses the one-shot command handlers.
func cmdRepl(ctx context.Context, client *spotify.Client, cfg *Config) error {
	fmt.Println("spoticli interactive mode - type 'help' or 'quit'")
	reader := bufio.NewReader(os.Stdin)
	for {
		fmt.Print("spoticli> ")
		line, err := reader.ReadString('\n')
		if err != nil { // EOF / Ctrl-D
			fmt.Println()
			return nil
		}
		fields := strings.Fields(strings.TrimSpace(line))
		if len(fields) == 0 {
			continue
		}
		cmd, rest := fields[0], fields[1:]
		var cerr error
		switch cmd {
		case "quit", "exit":
			return nil
		case "help", "?":
			replHelp()
		case "devices":
			cerr = cmdDevices(ctx, client)
		case "use":
			cerr = cmdUse(ctx, client, cfg, rest)
		case "play":
			cerr = cmdPlay(ctx, client, cfg, rest)
		case "pause":
			cerr = cmdPause(ctx, client, cfg)
		case "next", "skip":
			cerr = cmdNext(ctx, client, cfg)
		case "prev", "previous", "back":
			cerr = cmdPrev(ctx, client, cfg)
		case "search", "find":
			cerr = cmdSearch(ctx, client, rest)
		case "queue":
			cerr = cmdQueue(ctx, client, cfg, rest)
		case "volume", "vol":
			cerr = cmdVolume(ctx, client, cfg, rest)
		case "now", "status", "np":
			cerr = cmdNow(ctx, client)
		case "shuffle":
			cerr = cmdShuffle(ctx, client, cfg, rest)
		case "repeat":
			cerr = cmdRepeat(ctx, client, cfg, rest)
		default:
			fmt.Printf("unknown command: %s\n", cmd)
		}
		if cerr != nil {
			fmt.Fprintln(os.Stderr, "error:", cerr)
		}
	}
}

func replHelp() {
	fmt.Print(`commands:
  devices                 list Connect devices
  use <name>              pick the playback device
  play [query]            resume, or search+play a track
  pause                   pause
  next | prev             skip / go back
  search <query>          search tracks
  queue <query>           queue the top match
  volume <0-100>          set volume
  shuffle [on|off]        toggle shuffle
  repeat <off|track|context>
  now                     show what's playing
  quit                    leave
`)
}

func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	if n <= 1 {
		return s[:n]
	}
	return s[:n-1] + "\u2026"
}
