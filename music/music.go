package music

import (
	"context"
	"fmt"
	"io"
	"log"
	"net/http"
	"net/url"
	"os"
	"strings"
	"sync"
	"time"

	"github.com/grandcat/zeroconf"
	"github.com/zmb3/spotify/v2"
)

func check(err error) {
	if err != nil {
		fmt.Fprintln(os.Stderr, "error:", err)
		os.Exit(1)
	}
}

type DiscoveredDevice struct {
	Name     string
	IP       string
	Port     int
	Text     []string
	Service  string
	HostName string
	Domain   string
}

// findFirstLocalDevice monitors mDNS traffic to catch a Spotify Connect broadcast
func findFirstLocalDevice(ctx context.Context) (DiscoveredDevice, error) {
	resolver, err := zeroconf.NewResolver(nil)
	if err != nil {
		return DiscoveredDevice{}, err
	}

	entries := make(chan *zeroconf.ServiceEntry)
	var foundDev DiscoveredDevice
	var once sync.Once
	done := make(chan struct{})

	go func() {
		for entry := range entries {
			if len(entry.AddrIPv4) > 0 {
				once.Do(func() {
					foundDev = DiscoveredDevice{
						Name:     entry.Instance,
						IP:       entry.AddrIPv4[0].String(),
						Port:     entry.Port,
						Text:     entry.Text,
						Service:  entry.Service,
						HostName: entry.HostName,
						Domain:   entry.Domain,
					}
					close(done)
				})
			}
		}
	}()

	scanCtx, cancel := context.WithTimeout(ctx, 4*time.Second)
	defer cancel()

	err = resolver.Browse(scanCtx, "_spotify-connect._tcp", "local.", entries)
	if err != nil {
		return DiscoveredDevice{}, err
	}

	select {
	case <-done:
		return foundDev, nil
	case <-scanCtx.Done():
		return DiscoveredDevice{}, fmt.Errorf("mDNS discovery timed out")
	}
}

// wakeAndRegisterDevice executes the local Spirc login method
func wakeAndRegisterDevice(ctx context.Context, dev DiscoveredDevice, username string) error {
	endpoint := fmt.Sprintf("http://%s:%d/cpath?action=login", dev.IP, dev.Port)

	// Form payload representing standard Spotify Connect network parameters
	data := url.Values{}
	data.Set("action", "login")
	data.Set("userName", username)

	// 'blob' can remain empty for consumer receivers/TVs that are pre-linked,
	// or for standard open daemons expecting a public session kick.
	data.Set("blob", "")

	req, err := http.NewRequestWithContext(ctx, "POST", endpoint, strings.NewReader(data.Encode()))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")

	client := &http.Client{Timeout: 4 * time.Second}
	resp, err := client.Do(req)
	if err != nil {
		return fmt.Errorf("local network handshake failed: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		return fmt.Errorf("hardware rejected connection trigger (Status %d): %s", resp.StatusCode, string(body))
	}

	return nil
}

func InitMusic(ctx context.Context) {
	cfg, err := loadConfig()
	check(err)

	client, save, err := getClient(ctx, cfg)
	check(err)

	save()

	currentUser, err := client.CurrentUser(ctx)
	if err != nil {
		log.Fatalf("Could not fetch Spotify user data: %v", err)
	}
	log.Printf("Authenticated as Spotify User: %s\n", currentUser.ID)

	// 3. Scan the LAN for a sleeping or idle Spotify Connect device
	log.Println("Scanning LAN for Spotify Connect endpoints via mDNS...")
	localDev, err := findFirstLocalDevice(ctx)
	if err != nil {
		log.Fatalf("No local Spotify devices found on LAN: %v", err)
	}
	log.Printf("Found local hardware target: %s (%s:%d) %s %s %s\n", localDev.Name, localDev.IP, localDev.Port, localDev.Domain, localDev.Service, localDev.HostName)

	// 4. Force the local device to connect to Spotify Cloud
	log.Printf("Sending cloud activation signal to %s...\n", localDev.Name)
	err = wakeAndRegisterDevice(ctx, localDev, currentUser.ID)
	if err != nil {
		log.Printf("Warning during initial wake packet: %v", err)
	}

	// 5. Poll the Web API until the device registers and becomes visible
	log.Println("Waiting for device to register with Spotify cloud infrastructure...")
	var cloudDeviceID spotify.ID

	for i := 1; i <= 10; i++ {
		time.Sleep(2 * time.Second) // Give the hardware time to establish its TLS/MQTT connection

		devices, err := client.PlayerDevices(ctx)
		if err != nil {
			log.Printf("Error checking cloud registry: %v", err)
			continue
		}

		for _, d := range devices {
			// Cross-reference by matching names flexibly
			if strings.Contains(strings.ToLower(d.Name), strings.ToLower(localDev.Name)) ||
				strings.Contains(strings.ToLower(localDev.Name), strings.ToLower(d.Name)) {
				cloudDeviceID = d.ID
				break
			}
		}

		if cloudDeviceID != "" {
			log.Printf("🎉 Success! Device is now cloud-visible. Spotify Device ID: %s\n", cloudDeviceID)
			break
		}
		log.Printf("  [Attempt %d/10] Device not visible yet. Retrying...\n", i)
	}

	// if cloudDeviceID == "" {
	// 	log.Fatalln("Timeout: Device failed to report to Spotify Cloud API.")
	// }

	cmdDevices(ctx, client)
}
