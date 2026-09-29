package model

import "time"

// Device is a LAN client known to OpenPass.
type Device struct {
	ID        string    `json:"id"`
	IP        string    `json:"ip"`
	MAC       string    `json:"mac"`
	Hostname  string    `json:"hostname,omitempty"`
	Online    bool      `json:"online"`
	Hidden    bool      `json:"hidden"`
	Mode      string    `json:"mode"` // proxy, direct, blocked
	NodeID    string    `json:"node_id,omitempty"`
	DNS       string    `json:"dns,omitempty"`
	LastSeen  time.Time `json:"last_seen,omitempty"`
	FirstSeen time.Time `json:"first_seen,omitempty"`
}

type Node struct {
	ID               string    `json:"id"`
	Name             string    `json:"name"`
	Type             string    `json:"type"`
	Address          string    `json:"address"`
	Port             int       `json:"port,omitempty"`
	UUID             string    `json:"uuid,omitempty"`
	Password         string    `json:"password,omitempty"`
	Method           string    `json:"method,omitempty"`
	Flow             string    `json:"flow,omitempty"`
	Network          string    `json:"network,omitempty"`
	SNI              string    `json:"sni,omitempty"`
	RealityPublicKey string    `json:"reality_public_key,omitempty"`
	RealityShortID   string    `json:"reality_short_id,omitempty"`
	Fingerprint      string    `json:"fingerprint,omitempty"`
	Host             string    `json:"host,omitempty"`
	Path             string    `json:"path,omitempty"`
	TLS              bool      `json:"tls,omitempty"`
	URI              string    `json:"uri,omitempty"`
	Subscription     string    `json:"subscription,omitempty"`
	Enabled          bool      `json:"enabled"`
	Latency          int       `json:"latency,omitempty"`
	TCPReachable     *bool     `json:"tcp_reachable,omitempty"`
	URLReachable     *bool     `json:"url_reachable,omitempty"`
	CreatedAt        time.Time `json:"created_at"`
	UpdatedAt        time.Time `json:"updated_at"`
}

type DNS struct {
	ID   string `json:"id"`
	Name string `json:"name"`
	URL  string `json:"url"`
	Host string `json:"host,omitempty"`
}

type Settings struct {
	Enabled            bool   `json:"enabled"`
	KillSwitch         bool   `json:"kill_switch"`
	DefaultMode        string `json:"default_mode"`
	DefaultDNS         string `json:"default_dns"`
	URLTestAddress     string `json:"url_test_address"`
	URLTestRegion      string `json:"url_test_region"`
	WebPort            int    `json:"web_port"`
	AutoApply          bool   `json:"auto_apply"`
	SelfServiceEnabled bool   `json:"self_service_enabled"`
	HideAP             bool   `json:"hide_ap"`
	ForceDoH           bool   `json:"force_doh"`
	ProxyDNS           bool   `json:"proxy_dns"`
	DNSFailClosed      bool   `json:"dns_fail_closed"`
	CustomDNS          string `json:"custom_dns,omitempty"`
}

type State struct {
	Devices  []Device `json:"devices"`
	Nodes    []Node   `json:"nodes"`
	Settings Settings `json:"settings"`
	DNS      []DNS    `json:"dns"`
}

func DefaultDNS() []DNS {
	return []DNS{
		{ID: "cloudflare", Name: "Cloudflare DoH", URL: "https://cloudflare-dns.com/dns-query", Host: "cloudflare-dns.com"},
		{ID: "google", Name: "Google DoH", URL: "https://dns.google/dns-query", Host: "dns.google"},
		{ID: "aliyun", Name: "阿里 DoH", URL: "https://dns.alidns.com/dns-query", Host: "dns.alidns.com"},
		{ID: "tencent", Name: "腾讯 DoH", URL: "https://doh.pub/dns-query", Host: "doh.pub"},
	}
}

func DefaultSettings() Settings {
	return Settings{Enabled: false, KillSwitch: true, DefaultMode: "direct", DefaultDNS: "cloudflare", URLTestAddress: "https://www.gstatic.com/generate_204", URLTestRegion: "overseas", WebPort: 8787, AutoApply: true, SelfServiceEnabled: true, HideAP: true, ForceDoH: true, ProxyDNS: true, DNSFailClosed: true}
}
