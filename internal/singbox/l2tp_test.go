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
