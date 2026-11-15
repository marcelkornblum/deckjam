package server

import (
	"fmt"
	"net"
	"os"
	"path/filepath"
	"strings"
)

// DetectNetworkURLs discovers Cloudflare quick tunnel URLs (from public_url.txt)
// and non-loopback LAN IPv4 addresses, returning join_url and lan_urls.
func DetectNetworkURLs(port string, customHostname string, dataDir string) (string, []string) {
	if port == "" {
		port = "8080"
	}
	// Strip colon if passed like ":8080"
	port = strings.TrimPrefix(port, ":")

	var lanIPs []string

	ifaces, err := net.Interfaces()
	if err == nil {
		for _, iface := range ifaces {
			// Skip down or loopback interfaces
			if iface.Flags&net.FlagUp == 0 || iface.Flags&net.FlagLoopback != 0 {
				continue
			}
			addrs, err := iface.Addrs()
			if err != nil {
				continue
			}
			for _, addr := range addrs {
				ipnet, ok := addr.(*net.IPNet)
				if !ok {
					continue
				}
				ip := ipnet.IP.To4()
				if ip == nil || ip.IsLoopback() || ip.IsLinkLocalUnicast() {
					continue
				}
				lanIPs = append(lanIPs, ip.String())
			}
		}
	}

	var lanURLs []string
	var primaryJoinURL string

	// Check if public_url.txt exists in dataDir and contains a valid https://... URL
	if dataDir != "" {
		pubURLPath := filepath.Join(dataDir, "public_url.txt")
		if data, err := os.ReadFile(pubURLPath); err == nil {
			trimmed := strings.TrimSpace(string(data))
			if strings.HasPrefix(trimmed, "https://") {
				primaryJoinURL = trimmed
				lanURLs = append(lanURLs, trimmed)
			}
		}
	}

	for _, ip := range lanIPs {
		u := fmt.Sprintf("http://%s:%s", ip, port)
		lanURLs = append(lanURLs, u)
		if primaryJoinURL == "" {
			primaryJoinURL = u
		}
	}

	// Add steamdeck.local or customHostname
	mdnsHost := "steamdeck.local"
	if customHostname != "" {
		mdnsHost = customHostname
	}
	mdnsURL := fmt.Sprintf("http://%s:%s", mdnsHost, port)
	lanURLs = append(lanURLs, mdnsURL)

	if primaryJoinURL == "" {
		primaryJoinURL = mdnsURL
	}

	return primaryJoinURL, lanURLs
}
