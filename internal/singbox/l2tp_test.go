package singbox

import (
	"encoding/json"
	"strings"
	"testing"

	"openpass/internal/l2tp"
	"openpass/internal/model"
)

func TestL2TPDataBypassesCoreAndDNSBindsPPP(t *testing.T) {
	st := model.State{Settings: model.DefaultSettings(), DNS: model.DefaultDNS(), Nodes: []model.Node{{ID: "vpn", Type: "l2tp", Enabled: true, Password: "never-serialize-ppp-password"}}, Devices: []model.Device{{IP: "192.0.2.10", MAC: "02:00:00:00:00:01", Mode: "proxy", NodeID: "vpn", DNS: "cloudflare"}}}
	st.Settings.ProxyDNS = false // L2TP's isolation cannot be disabled by this legacy option.
	b, err := Build(st)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(b), st.Nodes[0].Password) {
		t.Fatal("PPP credentials appeared in sing-box config")
	}
	var cfg map[string]any
	if err = json.Unmarshal(b, &cfg); err != nil {
		t.Fatal(err)
	}
	tun := cfg["inbounds"].([]any)[0].(map[string]any)
	if tun["exclude_mac_address"].([]any)[0] != "02:00:00:00:00:01" {
		t.Fatal("L2TP traffic will enter the TUN")
	}
	if tun["exclude_uid"].([]any)[0] != float64(0) {
		t.Fatal("xl2tpd outer transport will be captured by the TUN")
	}
	out := cfg["outbounds"].([]any)[2].(map[string]any)
	if out["type"] != "direct" || out["bind_interface"] != l2tp.DeviceName("vpn") || out["server"] != nil {
		t.Fatal("DNS outbound is not bound to owned PPP")
	}
	servers := cfg["dns"].(map[string]any)["servers"].([]any)
	found := false
	for _, raw := range servers {
		server := raw.(map[string]any)
		if server["detour"] == "vpn" {
			found = true
		}
	}
	if !found {
		t.Fatal("L2TP DNS may use local WAN")
	}
	rules := cfg["route"].(map[string]any)["rules"].([]any)
	if rules[len(rules)-1].(map[string]any)["outbound"] != "block" {
		t.Fatal("unexpected TUN traffic must fail closed")
	}
	if _, err := ProbeConfig(st.Nodes[0], 12345, "cloudflare", nil); err == nil {
		t.Fatal("L2TP must not be tested as a sing-box protocol")
	}
}

func TestDisabledL2TPModuleRejectsDNSWithoutChangingOtherNodes(t *testing.T) {
	st := model.State{Settings: model.DefaultSettings(), DNS: model.DefaultDNS(), Nodes: []model.Node{
		{ID: "vpn", Type: "l2tp", Enabled: true},
		{ID: "socks", Type: "socks5", Enabled: true, Address: "192.0.2.50", Port: 1080},
	}, Devices: []model.Device{
		{IP: "192.0.2.10", MAC: "02:00:00:00:00:01", Mode: "proxy", NodeID: "vpn", DNS: "cloudflare"},
		{IP: "192.0.2.11", Mode: "proxy", NodeID: "socks", DNS: "cloudflare"},
	}}
	st.Settings.L2TPDisabled, st.Settings.ProxyDNS = true, false
	b, err := Build(st)
	if err != nil {
		t.Fatal(err)
	}
	var cfg map[string]any
	if err = json.Unmarshal(b, &cfg); err != nil {
		t.Fatal(err)
	}
	if !st.Nodes[0].Enabled {
		t.Fatal("configuration build mutated saved connection preference")
	}
	tun := cfg["inbounds"].([]any)[0].(map[string]any)
	if tun["exclude_uid"] != nil || tun["exclude_interface"] != nil || tun["exclude_mac_address"] != nil {
		t.Fatal("disabled module still changes TUN capture rules")
	}
	outs := cfg["outbounds"].([]any)
	if hasOutbound(outs, "vpn") || !hasOutbound(outs, "socks") {
		t.Fatal("wrong nodes were suspended")
	}
	dnsRules := cfg["dns"].(map[string]any)["rules"].([]any)
	blocked := false
	for _, raw := range dnsRules {
		rule := raw.(map[string]any)
		cidrs, _ := rule["source_ip_cidr"].([]any)
		if len(cidrs) > 0 && cidrs[0] == "192.0.2.10/32" {
			blocked = rule["action"] == "reject" && rule["server"] == nil
		}
	}
	if !blocked {
		t.Fatal("disabled L2TP client DNS falls back to WAN")
	}
}
