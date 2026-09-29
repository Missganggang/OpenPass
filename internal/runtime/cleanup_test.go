package runtime

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"openpass/internal/model"
)

func TestExplicitShutdownClearsPoliciesAndRejectsNewApply(t *testing.T) {
	t.Setenv("PATH", t.TempDir())
	dir := t.TempDir()
	rt := &Runtime{
		ConfigPath: filepath.Join(dir, "sing-box.json"), NFTPath: filepath.Join(dir, "live.nft"),
		FallbackNFTPath: filepath.Join(dir, "boot.nft"), DisabledPath: filepath.Join(dir, "service-disabled"),
	}
	st := model.State{Settings: model.DefaultSettings(), Devices: []model.Device{{IP: "192.0.2.25", Mode: "proxy"}}}
	st.Settings.Enabled = true
	if err := rt.writeFirewall(st); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(rt.DisabledPath, []byte("pending\n"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := rt.Shutdown(); err != nil {
		t.Fatal(err)
	}
	for _, path := range []string{rt.NFTPath, rt.FallbackNFTPath} {
		data, err := os.ReadFile(path)
		if err != nil || strings.Contains(string(data), "add ") || strings.Contains(string(data), "flush ") {
			t.Fatalf("disabled policy is not empty: %q, %v", data, err)
		}
	}
	if err := rt.Apply(st); err == nil || !strings.Contains(err.Error(), "service is stopped") {
		t.Fatalf("shutdown allowed a concurrent Apply: %v", err)
	}
	// Removing the marker must not revive a daemon already being terminated.
	if err := os.Remove(rt.DisabledPath); err != nil {
		t.Fatal(err)
	}
	if err := rt.Apply(st); err == nil || !strings.Contains(err.Error(), "service is stopped") {
		t.Fatalf("old daemon restarted after marker removal: %v", err)
	}
	if st.Devices[0].Mode != "proxy" || !st.Settings.Enabled {
		t.Fatal("service shutdown changed saved protection or device settings")
	}
}

func TestOrdinaryShutdownPreservesProtection(t *testing.T) {
	t.Setenv("PATH", t.TempDir())
	dir := t.TempDir()
	rt := &Runtime{NFTPath: filepath.Join(dir, "live.nft"), FallbackNFTPath: filepath.Join(dir, "boot.nft"), DisabledPath: filepath.Join(dir, "service-disabled")}
	st := model.State{Settings: model.DefaultSettings(), Devices: []model.Device{{IP: "192.0.2.25", Mode: "proxy"}}}
	st.Settings.Enabled = true
	if err := rt.writeFirewall(st); err != nil {
		t.Fatal(err)
	}
	if err := rt.Shutdown(); err != nil {
		t.Fatal(err)
	}
	boot, err := os.ReadFile(rt.FallbackNFTPath)
	if err != nil || !strings.Contains(string(boot), "blocked_clients { 192.0.2.25 }") {
		t.Fatalf("ordinary restart removed fail-closed policy: %q, %v", boot, err)
	}
	if err := rt.CleanupDisabled(); err == nil {
		t.Fatal("CLI cleanup accepted an enabled service")
	}
}

func TestDisableMarkerPreventsCrashMonitorPolicyRestore(t *testing.T) {
	t.Setenv("PATH", t.TempDir())
	dir := t.TempDir()
	rt := &Runtime{NFTPath: filepath.Join(dir, "live.nft"), FallbackNFTPath: filepath.Join(dir, "boot.nft"), DisabledPath: filepath.Join(dir, "service-disabled")}
	if err := os.WriteFile(rt.DisabledPath, nil, 0600); err != nil {
		t.Fatal(err)
	}
	st := model.State{Settings: model.DefaultSettings(), Devices: []model.Device{{IP: "192.0.2.25", Mode: "blocked"}}}
	st.Settings.Enabled = true
	// The monitor and a request already in flight share this final write gate.
	if err := rt.writeFirewall(st); err != nil {
		t.Fatal(err)
	}
	for _, path := range []string{rt.NFTPath, rt.FallbackNFTPath} {
		data, _ := os.ReadFile(path)
		if strings.Contains(string(data), "add rule") || strings.Contains(string(data), "add element") {
			t.Fatal("late monitor/request restored protection after explicit disable")
		}
	}
	// A repeated cleanup with no daemon or core is valid and leaves the
	// persistent marker intact for the next reboot.
	if err := rt.CleanupDisabled(); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(rt.DisabledPath); err != nil {
		t.Fatal("cleanup removed service-disable marker")
	}
}

func TestCleanupOnlyTouchesExistingOpenPassFirewallObjects(t *testing.T) {
	commands, err := cleanupCommands([]byte(`{"nftables":[
		{"chain":{"family":"inet","table":"fw4","name":"openpass_dns_nat"}},
		{"chain":{"family":"inet","table":"fw4","name":"openpass_forward"}},
		{"set":{"family":"inet","table":"fw4","name":"proxy_clients"}},
		{"chain":{"family":"inet","table":"fw4","name":"forward"}},
		{"chain":{"family":"inet","table":"passwall","name":"openpass_dns_nat"}},
		{"chain":{"family":"inet","table":"sing-box","name":"prerouting"}},
		{"chain":{"family":"inet","table":"dnsmasq","name":"prerouting"}},
		{"set":{"family":"inet","table":"passwall","name":"proxy_clients"}}
	]}`))
	if err != nil {
		t.Fatal(err)
	}
	want := "flush chain inet fw4 openpass_dns_nat\nflush chain inet fw4 openpass_forward\nflush set inet fw4 proxy_clients\n"
	if commands != want {
		t.Fatalf("cleanup targeted unexpected firewall objects: %q", commands)
	}
	commands, err = cleanupCommands([]byte(`{"nftables":[]}`))
	if err != nil || commands != "" {
		t.Fatalf("cleanup failed when no firewall is installed: %q %v", commands, err)
	}
}

func TestCleanupCoreOwnership(t *testing.T) {
	rt := &Runtime{SingBoxPath: "/usr/bin/sing-box", ConfigPath: "/var/etc/openpass/sing-box.json"}
	for _, tt := range []struct {
		args []string
		want bool
	}{
		{[]string{"/usr/bin/sing-box", "run", "-c", rt.ConfigPath}, true},
		{[]string{"sing-box", "run", "--config=" + rt.ConfigPath}, true},
		{[]string{"sing-box", "run", "-c", filepath.Join(os.TempDir(), "openpass-probe-123", "sing-box.json")}, true},
		{[]string{"sing-box", "run", "-c", "/etc/passwall/sing-box.json"}, false},
		{[]string{"sing-box", "run", "-c", "/etc/openpass-probe-other/sing-box.json"}, false},
		{[]string{"sing-box", "check", "-c", rt.ConfigPath}, false},
		{[]string{"another-daemon", "run", "-c", rt.ConfigPath}, false},
	} {
		if got := rt.ownsCore(tt.args); got != tt.want {
			t.Errorf("ownsCore(%q) = %v, want %v", tt.args, got, tt.want)
		}
	}
}
