package singbox

import (
	"encoding/json"
	"fmt"
	"net/url"
	"strings"

	"openpass/internal/model"
)

// Build creates a sing-box 1.8+ configuration. Device bindings become
// source_ip routing rules, while the default route follows the configured
// mode. DNS requests are sent through DoH and never use the LAN resolver.
func Build(st model.State) ([]byte, error) {
	dns := dnsProfile(st.Settings.DefaultDNS, st.DNS)
	defaultRoute := "direct"
	if st.Settings.DefaultMode == "blocked" || st.Settings.DefaultMode == "block" {
		defaultRoute = "block"
	}
	servers := make([]any, 0, 4)
	seenDNS := map[string]bool{}
	for _, server := range []map[string]any{dns, dnsProfile("aliyun", st.DNS), dnsProfile("cloudflare", st.DNS), dnsProfile("google", st.DNS), dnsProfile("tencent", st.DNS)} {
		tag, _ := server["tag"].(string)
		if !seenDNS[tag] {
			servers = append(servers, server)
			seenDNS[tag] = true
		}
	}
	dnsRules := make([]any, 0)
	// A LAN client can send DNS from a link-local IPv6 address.  The device
	// inventory currently keys bindings by IPv4, so there is no safe way to
	// associate that packet with the selected node.  Reject the IPv6 listener
	// by default instead of allowing it to fall through to the default (often
	// domestic) resolver.  Clients will use their IPv4 DNS path, which is
	// mapped to the per-device DoH detour above.
	dnsRules = append(dnsRules, map[string]any{"inbound": []string{"dns-local-v6"}, "action": "reject"})
	for _, d := range st.Devices {
		if d.IP == "" {
			continue
		}
		profile := d.DNS
		if profile == "" {
			// A proxy binding created from the self-service page does not
			// choose a DNS profile. Keep that traffic on an overseas resolver
			// by default; an administrator can still explicitly select Aliyun
			// or another profile for a device in the management page.
			if d.Mode == "proxy" && st.Settings.ProxyDNS {
				profile = "cloudflare"
			} else {
				profile = st.Settings.DefaultDNS
			}
		}
		server := dnsProfile(profile, st.DNS)
		tag, _ := server["tag"].(string)
		if d.Mode == "blocked" || d.Mode == "block" {
			dnsRules = append(dnsRules, map[string]any{"source_ip_cidr": []string{d.IP + "/32"}, "action": "reject"})
			continue
		}
		if d.Mode == "proxy" && st.Settings.ProxyDNS {
			validNode := false
			for _, node := range st.Nodes {
				if node.ID == d.NodeID && node.Enabled && outbound(node) != nil {
					validNode = true
					break
				}
			}
			if !validNode {
				// Router-local DNS stays reachable for management; a missing
				// proxy must still never resolve client names over the WAN.
				dnsRules = append(dnsRules, map[string]any{"source_ip_cidr": []string{d.IP + "/32"}, "action": "reject"})
				continue
			}
			tag = "proxy-" + tag + "-" + d.NodeID
			server["tag"], server["detour"] = tag, d.NodeID
			if !seenDNS[tag] {
				servers = append(servers, server)
				seenDNS[tag] = true
			}
		}
		dnsRules = append(dnsRules, map[string]any{"source_ip_cidr": []string{d.IP + "/32"}, "server": tag})
	}
	cfg := map[string]any{
		"log": map[string]any{"level": "info"},
		"dns": map[string]any{
			"servers": servers,
			"final":   dns["tag"], "strategy": "prefer_ipv4", "independent_cache": true,
		},
		"inbounds": []any{
			map[string]any{"type": "tun", "tag": "tun-in", "interface_name": "openpass0", "address": []string{"172.19.0.1/30"}, "auto_route": true, "auto_redirect": true, "strict_route": true, "stack": "system", "route_exclude_address": localNetworkExclusions()},
			map[string]any{"type": "direct", "tag": "dns-in", "listen": "0.0.0.0", "listen_port": 1053},
			map[string]any{"type": "direct", "tag": "dns-local-v6", "listen": "::1", "listen_port": 1054},
		},
		"outbounds": []any{map[string]any{"type": "direct", "tag": "direct"}, map[string]any{"type": "block", "tag": "block"}},
		"route":     map[string]any{"auto_detect_interface": true, "default_domain_resolver": dns["tag"], "final": defaultRoute, "rules": []any{}},
	}
	if len(dnsRules) > 0 {
		cfg["dns"].(map[string]any)["rules"] = dnsRules
	}
	outs := cfg["outbounds"].([]any)
	rules := cfg["route"].(map[string]any)["rules"].([]any)
	// LAN DNS packets are redirected by the nftables include to dns-in. The
	// hijack action makes sing-box resolve them with the selected DoH profile.
	rules = append(rules, map[string]any{"inbound": []string{"dns-in", "dns-local-v6"}, "action": "hijack-dns"})
	// Device-wide proxying must not send router administration or LAN peers
	// to a remote proxy. This also permits a client to repair its own binding
	// after its node fails. DNS is intercepted above, before this exception.
	rules = append(rules, map[string]any{"ip_is_private": true, "outbound": "direct"})
	for _, n := range st.Nodes {
		if !n.Enabled {
			continue
		}
		ob := outbound(n)
		if ob == nil {
			continue
		}
		outs = append(outs, ob)
	}
	for _, d := range st.Devices {
		if d.IP == "" {
			continue
		}
		tag := "direct"
		switch d.Mode {
		case "blocked":
			tag = "block"
		case "proxy":
			tag = "block"
			if d.NodeID != "" {
				tag = d.NodeID
			}
			if !hasOutbound(outs, tag) {
				tag = "block"
			}
		case "direct":
			tag = "direct"
		default:
			tag = "block"
		}
		rules = append(rules, map[string]any{"source_ip_cidr": []string{d.IP + "/32"}, "outbound": tag})
	}
	cfg["outbounds"] = outs
	cfg["route"].(map[string]any)["rules"] = rules
	return json.MarshalIndent(cfg, "", "  ")
}

func localNetworkExclusions() []string {
	return []string{"10.0.0.0/8", "172.16.0.0/12", "192.168.0.0/16", "127.0.0.0/8", "169.254.0.0/16", "::1/128", "fc00::/7", "fe80::/10"}
}

func hasOutbound(outs []any, tag string) bool {
	for _, raw := range outs {
		if o, ok := raw.(map[string]any); ok && o["tag"] == tag {
			return true
		}
	}
	return false
}

// dnsProfile uses a literal resolver IP as the HTTPS endpoint. This avoids a
// bootstrap lookup through the router's plaintext resolver. The TLS SNI still
// verifies the public DoH hostname.
func dnsProfile(id string, profiles []model.DNS) map[string]any {
	host, ip, path := "cloudflare-dns.com", "1.1.1.1", "/dns-query"
	switch id {
	case "aliyun":
		host, ip = "dns.alidns.com", "223.5.5.5"
	case "google":
		host, ip = "dns.google", "8.8.8.8"
	case "tencent":
		host, ip = "doh.pub", "119.29.29.29"
	default:
		id = "cloudflare"
	}
	for _, p := range profiles {
		if p.ID == id {
			if p.Host != "" {
				host = p.Host
			}
			if strings.HasPrefix(p.URL, "https://") {
				if u, err := url.Parse(p.URL); err == nil && u.Path != "" {
					path = u.Path
				}
			}
		}
	}
	return map[string]any{"type": "https", "tag": id, "server": ip, "path": path, "tls": map[string]any{"enabled": true, "server_name": host}}
}

func outbound(n model.Node) map[string]any {
	t := strings.ToLower(n.Type)
	o := map[string]any{"tag": n.ID, "server": n.Address, "server_port": n.Port}
	switch t {
	case "vless":
		o["type"] = "vless"
		o["uuid"] = n.UUID
		if n.Flow != "" {
			o["flow"] = n.Flow
		}
	case "vmess":
		o["type"] = "vmess"
		o["uuid"] = n.UUID
		o["security"] = "auto"
	case "trojan":
		o["type"] = "trojan"
		o["password"] = n.Password
	case "socks5":
		o["type"] = "socks"
		o["version"] = "5"
		if n.UUID != "" {
			o["username"] = n.UUID
		}
		if n.Password != "" {
			o["password"] = n.Password
		}
	case "http":
		o["type"] = "http"
		if n.UUID != "" {
			o["username"] = n.UUID
		}
		if n.Password != "" {
			o["password"] = n.Password
		}
	case "shadowsocks":
		o["type"] = "shadowsocks"
		o["method"] = n.Method
		o["password"] = n.Password
	default:
		return nil
	}
	if n.TLS || n.SNI != "" || n.RealityPublicKey != "" {
		tls := map[string]any{"enabled": true}
		if n.SNI != "" {
			tls["server_name"] = n.SNI
		}
		if n.Fingerprint != "" {
			tls["utls"] = map[string]any{"enabled": true, "fingerprint": n.Fingerprint}
		}
		if n.RealityPublicKey != "" {
			tls["reality"] = map[string]any{"enabled": true, "public_key": n.RealityPublicKey, "short_id": n.RealityShortID}
		}
		o["tls"] = tls
	}
	if strings.EqualFold(n.Network, "grpc") {
		transport := map[string]any{"type": "grpc"}
		if n.Path != "" {
			transport["service_name"] = strings.TrimPrefix(n.Path, "/")
		}
		o["transport"] = transport
	} else if strings.EqualFold(n.Network, "ws") || (n.Network == "" && (n.Host != "" || n.Path != "")) {
		h := map[string]any{}
		if n.Host != "" {
			h["Host"] = n.Host
		}
		transport := map[string]any{"type": "ws", "headers": h}
		if n.Path != "" {
			transport["path"] = n.Path
		}
		o["transport"] = transport
	}
	return o
}

// ProbeConfig uses the same outbound as normal traffic, but never creates a
// TUN interface or changes routes/firewall rules. All test traffic is confined
// to an ephemeral loopback HTTP/SOCKS listener and this one node.
func ProbeConfig(n model.Node, port int, dnsID string, profiles []model.DNS) ([]byte, error) {
	n.ID = "probe-node"
	o := outbound(n)
	if o == nil {
		return nil, fmt.Errorf("unsupported node protocol %q", n.Type)
	}
	bootstrap := dnsProfile(dnsID, profiles)
	bootstrap["tag"] = "bootstrap"
	proxyDNS := dnsProfile(dnsID, profiles)
	proxyDNS["tag"], proxyDNS["detour"] = "proxy-dns", "probe-node"
	cfg := map[string]any{
		"log":       map[string]any{"level": "error", "timestamp": false},
		"dns":       map[string]any{"servers": []any{bootstrap, proxyDNS}, "final": "proxy-dns", "strategy": "prefer_ipv4"},
		"inbounds":  []any{map[string]any{"type": "mixed", "tag": "probe-in", "listen": "127.0.0.1", "listen_port": port}},
		"outbounds": []any{o},
		"route":     map[string]any{"default_domain_resolver": "bootstrap", "final": "probe-node"},
	}
	return json.Marshal(cfg)
}

// DoHEndpoint returns the pinned IP and certificate name used by sing-box.
// Node probes use it as well, so bootstrap never falls back to plaintext DNS.
func DoHEndpoint(id string, profiles []model.DNS) (ip, host, path string) {
	p := dnsProfile(id, profiles)
	return p["server"].(string), p["tls"].(map[string]any)["server_name"].(string), p["path"].(string)
}

func Validate(st model.State) error {
	b, e := Build(st)
	if e != nil {
		return e
	}
	if len(b) == 0 {
		return fmt.Errorf("empty config")
	}
	return nil
}
