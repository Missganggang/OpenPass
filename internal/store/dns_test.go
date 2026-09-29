package store

import (
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"testing"

	"openpass/internal/model"
)

func TestLoadMergesDNSPresetsWithoutChangingBindingsOrSettings(t *testing.T) {
	settings := model.DefaultSettings()
	settings.Enabled = true
	settings.DefaultDNS = "google"
	settings.DefaultMode = "blocked"
	settings.WebPort = 9988
	settings.CustomDNS = "https://resolver.example/dns-query"
	settings.ProxyDNS = false
	custom := model.DNS{ID: "private", Name: "Office resolver", URL: "https://resolver.example/office", Host: "resolver.example"}
	state := model.State{
		Settings: settings,
		Devices: []model.Device{
			{ID: "direct", IP: "10.0.0.2", Mode: "direct", DNS: "aliyun"},
			{ID: "proxy", IP: "10.0.0.3", Mode: "proxy", NodeID: "node", DNS: "cloudflare"},
		},
		Nodes: []model.Node{{ID: "node", Type: "socks5", Address: "node.example", Port: 1080, Enabled: true}},
		DNS: []model.DNS{
			{ID: "aliyun", Name: "阿里 DoH", URL: "https://dns.alidns.com/old-path", Host: "existing.example"},
			{ID: "cloudflare", Name: "Cloudflare DoH", URL: "https://cloudflare-dns.com/dns-query", Host: "cloudflare-dns.com"},
			custom,
		},
	}
	path := filepath.Join(t.TempDir(), "state.json")
	b, err := json.Marshal(state)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, b, 0600); err != nil {
		t.Fatal(err)
	}
	s, err := New(path)
	if err != nil {
		t.Fatal(err)
	}
	got := s.State()
	if !reflect.DeepEqual(got.Settings, settings) || !reflect.DeepEqual(got.Devices, state.Devices) || !reflect.DeepEqual(got.Nodes, state.Nodes) {
		t.Fatal("DNS upgrade changed settings, node credentials or device bindings")
	}
	profiles := make(map[string]model.DNS)
	for _, profile := range got.DNS {
		if _, exists := profiles[profile.ID]; exists {
			t.Fatalf("duplicate DNS profile %q", profile.ID)
		}
		profiles[profile.ID] = profile
	}
	for _, preset := range model.DefaultDNS() {
		if got, ok := profiles[preset.ID]; !ok || got.Name != preset.Name {
			t.Fatalf("preset %q missing or label outdated: %+v", preset.ID, got)
		}
	}
	if profiles["private"] != custom {
		t.Fatal("custom DNS profile was overwritten")
	}
	if got := profiles["aliyun"]; got.URL != state.DNS[0].URL || got.Host != state.DNS[0].Host {
		t.Fatal("existing profile endpoint was overwritten", got)
	}
	if len(got.DNS) != len(model.DefaultDNS())+1 {
		t.Fatal("unexpected number of merged profiles", got.DNS)
	}
	if err := s.UpdateSettings(settings); err != nil {
		t.Fatal(err)
	}
	reloaded, err := New(path)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(reloaded.State(), got) {
		t.Fatal("DNS migration must be idempotent after saving and reloading")
	}
}

func TestLoadMissingDNSDefaultPreservesOtherSettings(t *testing.T) {
	path := filepath.Join(t.TempDir(), "state.json")
	if err := os.WriteFile(path, []byte(`{"settings":{"default_dns":"","enabled":true,"default_mode":"blocked","web_port":9988,"proxy_dns":false},"dns":[]}`), 0600); err != nil {
		t.Fatal(err)
	}
	s, err := New(path)
	if err != nil {
		t.Fatal(err)
	}
	got := s.Settings()
	if got.DefaultDNS != "aliyun" || !got.Enabled || got.DefaultMode != "blocked" || got.WebPort != 9988 || got.ProxyDNS {
		t.Fatal("filling a DNS default must not reset other preferences", got)
	}
	if len(s.DNS()) != len(model.DefaultDNS()) {
		t.Fatal("empty DNS list was not populated with current presets")
	}
}
