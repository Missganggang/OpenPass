package model

import (
	"encoding/json"
	"time"
)

// Device is a LAN client known to OpenPass.
type Device struct {
	ID        string    `json:"id"`
	IP        string    `json:"ip"`
	MAC       string    `json:"mac"`
	Hostname  string    `json:"hostname,omitempty"`
	Remark    string    `json:"remark,omitempty"`
	Online    bool      `json:"online"`
	Hidden    bool      `json:"hidden"`
	Mode      string    `json:"mode"` // proxy, direct, blocked
	NodeID    string    `json:"node_id,omitempty"`
	DNS       string    `json:"dns,omitempty"`
	LastSeen  time.Time `json:"last_seen,omitempty"`
	FirstSeen time.Time `json:"first_seen,omitempty"`
}

type Node struct {
	ID   string `json:"id"`
	Name string `json:"name"`
	// Subscription links nodes imported from a named remote feed. Empty means
	// the node was imported or created independently.
	Subscription string `json:"subscription_id,omitempty"`
	// Remark is an operator supplied note (for example, the provider or
	// intended location). It is kept separately from Name so imports can keep
	// their display label while administrators annotate a node.
	Remark           string    `json:"remark,omitempty"`
	Type             string    `json:"type"`
	Address          string    `json:"address"`
	Port             int       `json:"port,omitempty"`
	UUID             string    `json:"uuid,omitempty"`
	Password         string    `json:"password,omitempty"`
	Username         string    `json:"username,omitempty"`
	MTU              int       `json:"mtu,omitempty"`
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
	Enabled          bool      `json:"enabled"`
	Latency          int       `json:"latency,omitempty"`
	TCPReachable     *bool     `json:"tcp_reachable,omitempty"`
	URLReachable     *bool     `json:"url_reachable,omitempty"`
	CreatedAt        time.Time `json:"created_at"`
	UpdatedAt        time.Time `json:"updated_at"`
}

// UnmarshalJSON accepts the pre-subscription state key as well. Early
// development builds used "subscription" while the public API now exposes
// the clearer "subscription_id" name; normal writes always use the latter.
func (n *Node) UnmarshalJSON(data []byte) error {
	type nodeAlias Node
	var aux struct {
		*nodeAlias
		LegacySubscription string `json:"subscription"`
	}
	aux.nodeAlias = (*nodeAlias)(n)
	if err := json.Unmarshal(data, &aux); err != nil {
		return err
	}
	if n.Subscription == "" {
		n.Subscription = aux.LegacySubscription
	}
	return nil
}

// Subscription is a named remote node feed. Nodes imported from the feed keep
// the subscription ID in Node.Subscription so a refresh or removal can update
// only that feed's nodes without affecting manually imported nodes.
type Subscription struct {
	ID         string    `json:"id"`
	Name       string    `json:"name"`
	Remark     string    `json:"remark,omitempty"`
	URL        string    `json:"url"`
	Enabled    bool      `json:"enabled"`
	LastSyncAt time.Time `json:"last_sync_at,omitempty"`
	LastError  string    `json:"last_error,omitempty"`
	CreatedAt  time.Time `json:"created_at"`
	UpdatedAt  time.Time `json:"updated_at"`
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
	// False preserves the enabled L2TP module on installations predating this flag.
	L2TPDisabled bool `json:"l2tp_disabled"`
}

type State struct {
	Devices       []Device       `json:"devices"`
	Nodes         []Node         `json:"nodes"`
	Subscriptions []Subscription `json:"subscriptions"`
	Settings      Settings       `json:"settings"`
	DNS           []DNS          `json:"dns"`
}

func DefaultDNS() []DNS {
	return []DNS{
		{ID: "cloudflare", Name: "Cloudflare DoH · 1.1.1.1", URL: "https://cloudflare-dns.com/dns-query", Host: "cloudflare-dns.com"},
		{ID: "google", Name: "Google DoH · 8.8.8.8", URL: "https://dns.google/dns-query", Host: "dns.google"},
		{ID: "aliyun", Name: "阿里 DoH · 223.5.5.5", URL: "https://dns.alidns.com/dns-query", Host: "dns.alidns.com"},
		{ID: "aliyun-secondary", Name: "阿里 DoH · 223.6.6.6", URL: "https://dns.alidns.com/dns-query", Host: "dns.alidns.com"},
		{ID: "tencent", Name: "腾讯 DoH · 120.53.53.53", URL: "https://doh.pub/dns-query", Host: "doh.pub"},
	}
}

func DefaultSettings() Settings {
	return Settings{Enabled: false, KillSwitch: true, DefaultMode: "direct", DefaultDNS: "aliyun", URLTestAddress: "https://www.gstatic.com/generate_204", URLTestRegion: "overseas", WebPort: 8787, AutoApply: true, SelfServiceEnabled: true, HideAP: true, ForceDoH: true, ProxyDNS: true, DNSFailClosed: true}
}
