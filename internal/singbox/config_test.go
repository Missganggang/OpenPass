package singbox

import (
	"encoding/json"
	"testing"

	"openpass/internal/model"
)

func TestProbeConfigHasNoDirectFallbackOrSystemRoutes(t *testing.T) {
	for _, protocol := range []string{"socks5", "vless", "vmess", "trojan", "http", "shadowsocks"} {
		t.Run(protocol, func(t *testing.T) {
			b, err := ProbeConfig(model.Node{Type: protocol, Address: "node.invalid", Port: 443, UUID: "user", Password: "secret"}, 19090, "aliyun", nil)
			if err != nil {
				t.Fatal(err)
			}
			var cfg map[string]any
			if err := json.Unmarshal(b, &cfg); err != nil {
				t.Fatal(err)
			}
			ins := cfg["inbounds"].([]any)
			inbound := ins[0].(map[string]any)
			if len(ins) != 1 || inbound["type"] != "mixed" || inbound["listen"] != "127.0.0.1" {
				t.Fatal("probe must only listen on loopback", inbound)
			}
			outs := cfg["outbounds"].([]any)
			if len(outs) != 1 || outs[0].(map[string]any)["tag"] != "probe-node" {
				t.Fatal("unexpected fallback outbound", outs)
			}
			route := cfg["route"].(map[string]any)
			if route["final"] != "probe-node" || route["auto_detect_interface"] != nil {
				t.Fatal("unsafe probe routing", route)
			}
			for _, raw := range cfg["dns"].(map[string]any)["servers"].([]any) {
				dns := raw.(map[string]any)
				if dns["type"] != "https" || dns["server"] != "223.5.5.5" {
					t.Fatal("probe DNS must use literal DoH", dns)
				}
			}
		})
	}
}

func TestWebSocketPathIsNotAHeader(t *testing.T) {
	o := outbound(model.Node{Type: "vless", Network: "ws", Host: "cdn.example", Path: "/websocket"})
	transport := o["transport"].(map[string]any)
	if transport["path"] != "/websocket" {
		t.Fatal("WebSocket path was lost", transport)
	}
	headers := transport["headers"].(map[string]any)
	if headers["Host"] != "cdn.example" || headers["path"] != nil {
		t.Fatal("incorrect WebSocket headers", headers)
	}
	if outbound(model.Node{Type: "vless", Network: "tcp", Host: "cdn.example"})["transport"] != nil {
		t.Fatal("TCP must not silently become WebSocket")
	}
}

func TestUnboundProxyFailsClosedWhileNewDevicesDefaultDirect(t *testing.T) {
	st := model.State{Settings: model.DefaultSettings(), Devices: []model.Device{{IP: "192.0.2.10", Mode: "proxy"}, {IP: "192.0.2.11", Mode: "proxy", NodeID: "missing"}}}
	b, err := Build(st)
	if err != nil {
		t.Fatal(err)
	}
	var cfg map[string]any
	if err := json.Unmarshal(b, &cfg); err != nil {
		t.Fatal(err)
	}
	route := cfg["route"].(map[string]any)
	if route["final"] != "direct" {
		t.Fatal("new-device direct default was overridden by kill switch")
	}
	for _, raw := range route["rules"].([]any) {
		rule := raw.(map[string]any)
		if rule["source_ip_cidr"] != nil && rule["outbound"] != "block" {
			t.Fatal("proxy device without a valid node leaked to direct", raw)
		}
	}
}

func TestRouterManagementStaysLocalBeforeDeviceBinding(t *testing.T) {
	st := model.State{Settings: model.DefaultSettings(), Nodes: []model.Node{{ID: "node", Type: "socks5", Address: "node.example", Port: 1080, Enabled: true}}, Devices: []model.Device{{IP: "10.0.0.225", Mode: "proxy", NodeID: "node"}}}
	b, err := Build(st)
	if err != nil {
		t.Fatal(err)
	}
	var cfg map[string]any
	if err := json.Unmarshal(b, &cfg); err != nil {
		t.Fatal(err)
	}
	inbound := cfg["inbounds"].([]any)[0].(map[string]any)
	exclusions := inbound["route_exclude_address"].([]any)
	for _, required := range []string{"10.0.0.0/8", "192.168.0.0/16", "172.16.0.0/12"} {
		found := false
		for _, raw := range exclusions {
			if raw == required {
				found = true
			}
		}
		if !found {
			t.Errorf("TUN captures LAN management network %s", required)
		}
	}
	rules := cfg["route"].(map[string]any)["rules"].([]any)
	if rules[0].(map[string]any)["action"] != "hijack-dns" {
		t.Fatal("DNS interception must precede local exception")
	}
	if local := rules[1].(map[string]any); local["ip_is_private"] != true || local["outbound"] != "direct" {
		t.Fatal("management exception must precede source proxy binding", local)
	}
	if rules[2].(map[string]any)["outbound"] != "node" {
		t.Fatal("public traffic must still use selected node")
	}
}
