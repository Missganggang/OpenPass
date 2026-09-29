package singbox

import (
	"encoding/json"
	"testing"

	"openpass/internal/model"
)

// An overseas DNS default must not prevent the router from resolving the
// proxy's own hostname before that proxy can be connected.
func TestProbeOverseasDNSKeepsReachableBootstrap(t *testing.T) {
	for _, tc := range []struct {
		profile string
		server  string
		host    string
	}{
		{profile: "cloudflare", server: "1.1.1.1", host: "cloudflare-dns.com"},
		{profile: "google", server: "8.8.8.8", host: "dns.google"},
	} {
		t.Run(tc.profile, func(t *testing.T) {
			b, err := ProbeConfig(model.Node{Type: "socks5", Address: "node.example", Port: 1080}, 19090, tc.profile, model.DefaultDNS())
			if err != nil {
				t.Fatal(err)
			}
			var cfg struct {
				DNS struct {
					Servers []struct {
						Type   string `json:"type"`
						Tag    string `json:"tag"`
						Server string `json:"server"`
						Detour string `json:"detour"`
						TLS    struct {
							Enabled    bool   `json:"enabled"`
							ServerName string `json:"server_name"`
						} `json:"tls"`
					} `json:"servers"`
					Final string `json:"final"`
				} `json:"dns"`
				Route struct {
					DefaultDomainResolver string `json:"default_domain_resolver"`
				} `json:"route"`
			}
			if err := json.Unmarshal(b, &cfg); err != nil {
				t.Fatal(err)
			}
			if cfg.Route.DefaultDomainResolver != "bootstrap" || cfg.DNS.Final != "proxy-dns" {
				t.Fatalf("node bootstrap and target DNS must stay separate: %s", b)
			}
			found := map[string]bool{}
			for _, server := range cfg.DNS.Servers {
				found[server.Tag] = true
				if server.Type != "https" || !server.TLS.Enabled {
					t.Fatalf("probe resolver must use verified DoH: %+v", server)
				}
				switch server.Tag {
				case "bootstrap":
					if server.Server != "223.5.5.5" || server.TLS.ServerName != "dns.alidns.com" || server.Detour != "" {
						t.Fatalf("node bootstrap must remain reachable before the proxy starts: %+v", server)
					}
				case "proxy-dns":
					if server.Server != tc.server || server.TLS.ServerName != tc.host || server.Detour != "probe-node" {
						t.Fatalf("selected DNS must remain behind the tested node: %+v", server)
					}
				default:
					t.Fatalf("unexpected resolver: %+v", server)
				}
			}
			if len(cfg.DNS.Servers) != 2 || !found["bootstrap"] || !found["proxy-dns"] {
				t.Fatalf("probe must provide both bootstrap and proxied DNS: %s", b)
			}
		})
	}
}
