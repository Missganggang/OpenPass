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
	first := rules[0].(map[string]any)
	if first["action"] != "reject" {
		t.Fatal("unmapped IPv6 DNS must fail closed", first)
	}
	if inbound := first["inbound"].([]any); len(inbound) != 1 || inbound[0] != "dns-local-v6" {
		t.Fatal("IPv6 DNS guard must target the local-v6 listener", first)
	}
	for i, node := range []string{"one", "two"} {
		rule := rules[i+1].(map[string]any)
		server := servers[rule["server"].(string)]
		if server["detour"] != node || server["type"] != "https" || server["server"] != "223.5.5.5" {
			t.Fatalf("device DNS escaped node %s: %v", node, server)
		}
	}
	if rules[1].(map[string]any)["server"] == rules[2].(map[string]any)["server"] {
		t.Fatal("different proxy devices share a resolver outbound")
	}
	direct := servers[rules[3].(map[string]any)["server"].(string)]
	if direct["detour"] != nil {
		t.Fatal("direct device DNS must remain direct", direct)
	}
	for _, raw := range rules[4:] {
		if raw.(map[string]any)["action"] != "reject" {
			t.Fatal("blocked/missing proxy leaked DNS", raw)
		}
	}
	bootstrapTag := cfg["route"].(map[string]any)["default_domain_resolver"].(string)
	if bootstrap := servers[bootstrapTag]; bootstrap["detour"] != nil || bootstrap["type"] != "https" || bootstrap["server"] != "223.5.5.5" {
		t.Fatal("node bootstrap must avoid recursion and plaintext", bootstrap)
	}
	if dns["independent_cache"] != true {
		t.Fatal("DNS cache must be isolated per outbound resolver")
	}
}

func TestUnspecifiedProxyDNSDefaultsOverseas(t *testing.T) {
	st := model.State{Settings: model.DefaultSettings(), Nodes: []model.Node{{ID: "node", Type: "socks5", Address: "node.example", Port: 1080, Enabled: true}}, Devices: []model.Device{{IP: "10.0.0.30", Mode: "proxy", NodeID: "node"}}}
	st.Settings.DefaultDNS = "aliyun"
	b, err := Build(st)
	if err != nil {
		t.Fatal(err)
	}
	var cfg map[string]any
	if err := json.Unmarshal(b, &cfg); err != nil {
		t.Fatal(err)
	}
	servers := map[string]map[string]any{}
	for _, raw := range cfg["dns"].(map[string]any)["servers"].([]any) {
		server := raw.(map[string]any)
		servers[server["tag"].(string)] = server
	}
	rule := cfg["dns"].(map[string]any)["rules"].([]any)[1].(map[string]any)
	server := servers[rule["server"].(string)]
	if server["detour"] != "node" || server["server"] != "1.1.1.1" {
		t.Fatalf("unspecified proxy DNS must use Cloudflare through the node: %#v", server)
	}
}

func TestDeviceDNSDefaultsStaySeparateFromRouterDefault(t *testing.T) {
	st := model.State{Settings: model.DefaultSettings(), DNS: model.DefaultDNS(), Nodes: []model.Node{{ID: "node", Type: "socks5", Address: "node.example", Port: 1080, Enabled: true}}, Devices: []model.Device{
		{IP: "10.0.0.30", Mode: "direct"},
		{IP: "10.0.0.31", Mode: "proxy", NodeID: "node"},
		{IP: "10.0.0.32", Mode: "direct", DNS: "tencent"},
		{IP: "10.0.0.33", Mode: "proxy", NodeID: "node", DNS: "aliyun-secondary"},
	}}
	st.Settings.DefaultDNS = "google"
	b, err := Build(st)
	if err != nil {
		t.Fatal(err)
	}
	var cfg map[string]any
	if err := json.Unmarshal(b, &cfg); err != nil {
		t.Fatal(err)
	}
	dns := cfg["dns"].(map[string]any)
	servers := make(map[string]map[string]any)
	for _, raw := range dns["servers"].([]any) {
		server := raw.(map[string]any)
		servers[server["tag"].(string)] = server
		if server["type"] != "https" || server["tls"].(map[string]any)["enabled"] != true {
			t.Fatal("DNS presets must not downgrade to plaintext", server)
		}
	}
	for i, want := range []struct {
		server string
		detour any
	}{
		{server: "223.5.5.5"},
		{server: "1.1.1.1", detour: "node"},
		{server: "120.53.53.53"},
		{server: "223.6.6.6", detour: "node"},
	} {
		rule := dns["rules"].([]any)[i+1].(map[string]any)
		server := servers[rule["server"].(string)]
		if server["server"] != want.server || server["detour"] != want.detour {
			t.Fatalf("device %d DNS policy = %+v, want %+v", i, server, want)
		}
	}
	secondary := servers["aliyun-secondary"]
	if secondary["server"] != "223.6.6.6" || secondary["tls"].(map[string]any)["server_name"] != "dns.alidns.com" {
		t.Fatal("Aliyun secondary must use its own IP with verified DoH hostname", secondary)
	}
	if bootstrap := servers["bootstrap"]; bootstrap["server"] != "223.5.5.5" || bootstrap["detour"] != nil {
		t.Fatal("node endpoint bootstrap must stay on primary Aliyun DoH", bootstrap)
	}
	if dns["final"] != "google" {
		t.Fatal("device defaults changed the router's configured DNS")
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
