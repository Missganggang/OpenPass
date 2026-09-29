package singbox

import (
	"encoding/json"
	"testing"

	"openpass/internal/model"
)

func TestProxyDNSUsesBoundNodeAndSeparateBootstrap(t *testing.T) {
	st := model.State{Settings: model.DefaultSettings(), Nodes: []model.Node{
		{ID: "one", Type: "socks5", Address: "one.example", Port: 1080, Enabled: true},
		{ID: "two", Type: "vless", Address: "two.example", Port: 443, Enabled: true},
	}, Devices: []model.Device{
		{IP: "10.0.0.20", Mode: "proxy", NodeID: "one", DNS: "aliyun"},
		{IP: "10.0.0.21", Mode: "proxy", NodeID: "two", DNS: "aliyun"},
		{IP: "10.0.0.22", Mode: "direct", DNS: "aliyun"},
		{IP: "10.0.0.23", Mode: "proxy", NodeID: "missing", DNS: "aliyun"},
		{IP: "10.0.0.24", Mode: "blocked", DNS: "aliyun"},
	}}
	b, err := Build(st)
	if err != nil {
		t.Fatal(err)
	}
	var cfg map[string]any
	if err := json.Unmarshal(b, &cfg); err != nil {
		t.Fatal(err)
	}
	dns := cfg["dns"].(map[string]any)
	servers := map[string]map[string]any{}
	for _, raw := range dns["servers"].([]any) {
		server := raw.(map[string]any)
		servers[server["tag"].(string)] = server
	}
	rules := dns["rules"].([]any)
	for i, node := range []string{"one", "two"} {
		rule := rules[i].(map[string]any)
		server := servers[rule["server"].(string)]
		if server["detour"] != node || server["type"] != "https" || server["server"] != "223.5.5.5" {
			t.Fatalf("device DNS escaped node %s: %v", node, server)
		}
	}
	if rules[0].(map[string]any)["server"] == rules[1].(map[string]any)["server"] {
		t.Fatal("different proxy devices share a resolver outbound")
	}
	direct := servers[rules[2].(map[string]any)["server"].(string)]
	if direct["detour"] != nil {
		t.Fatal("direct device DNS must remain direct", direct)
	}
	for _, raw := range rules[3:] {
		if raw.(map[string]any)["action"] != "reject" {
			t.Fatal("blocked/missing proxy leaked DNS", raw)
		}
	}
	bootstrapTag := cfg["route"].(map[string]any)["default_domain_resolver"].(string)
	if bootstrap := servers[bootstrapTag]; bootstrap["detour"] != nil || bootstrap["type"] != "https" {
		t.Fatal("node bootstrap must avoid recursion and plaintext", bootstrap)
	}
	if dns["independent_cache"] != true {
		t.Fatal("DNS cache must be isolated per outbound resolver")
	}
}

func TestRouterDNSHasIPv4AndIPv6LoopbackListeners(t *testing.T) {
	b, err := Build(model.State{Settings: model.DefaultSettings()})
	if err != nil {
		t.Fatal(err)
	}
	var cfg map[string]any
	if err := json.Unmarshal(b, &cfg); err != nil {
		t.Fatal(err)
	}
	listeners := map[string]map[string]any{}
	for _, raw := range cfg["inbounds"].([]any) {
		in := raw.(map[string]any)
		listeners[in["tag"].(string)] = in
	}
	if in := listeners["dns-in"]; in["listen"] != "0.0.0.0" || in["listen_port"] != float64(1053) {
		t.Fatal("IPv4 DNS listener unavailable", in)
	}
	if in := listeners["dns-local-v6"]; in["listen"] != "::1" || in["listen_port"] != float64(1054) {
		t.Fatal("IPv6 DNS must listen on loopback", in)
	}
	rule := cfg["route"].(map[string]any)["rules"].([]any)[0].(map[string]any)
	if rule["action"] != "hijack-dns" || len(rule["inbound"].([]any)) != 2 {
		t.Fatal("both DNS listeners must use DoH", rule)
	}
}
