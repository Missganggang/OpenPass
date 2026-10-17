package runtime

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"openpass/internal/model"
)

func TestApplyProtectsNewProxyBindingBeforeEarlyFailures(t *testing.T) {
	for _, scenario := range []string{"missing L2TP dependencies", "config write failure", "core config check failure"} {
		t.Run(scenario, func(t *testing.T) {
			// No host firewall/network tools are permitted in this fixture.
			t.Setenv("PATH", t.TempDir())
			dir := t.TempDir()
			rt := &Runtime{
				NFTPath: filepath.Join(dir, "live.nft"), FallbackNFTPath: filepath.Join(dir, "boot.nft"),
				ConfigPath: filepath.Join(dir, "sing-box.json"), SingBoxPath: filepath.Join(dir, "missing-core"),
			}
			st := model.State{Settings: model.DefaultSettings(), Devices: []model.Device{
				{ID: "new-binding", IP: "192.0.2.25", MAC: "02:00:00:00:00:25", Mode: "direct"},
				{ID: "unchanged-direct", IP: "192.0.2.26", MAC: "02:00:00:00:00:26", Mode: "direct"},
			}}
			st.Settings.Enabled = true
			if err := rt.writeFirewall(st); err != nil {
				t.Fatal(err)
			}
			st.Devices[0].Mode, st.Devices[0].NodeID = "proxy", "selected-node"
			wantError := ""
			switch scenario {
			case "missing L2TP dependencies":
				st.Nodes = []model.Node{{ID: "selected-node", Type: "l2tp", Enabled: true}}
				wantError = "L2TP 缺少依赖"
			case "config write failure":
				blocker := filepath.Join(dir, "not-a-directory")
				if err := os.WriteFile(blocker, []byte("block"), 0600); err != nil {
					t.Fatal(err)
				}
				rt.ConfigPath = filepath.Join(blocker, "sing-box.json")
			case "core config check failure":
				// A corrupt executable is discoverable but cannot pass its config
				// check. This works without shell helpers on Linux and Windows.
				rt.SingBoxPath = filepath.Join(dir, "broken-core.exe")
				if err := os.WriteFile(rt.SingBoxPath, []byte("not an executable"), 0755); err != nil {
					t.Fatal(err)
				}
				wantError = "sing-box config check failed"
			}
			if err := rt.Apply(st); err == nil || (wantError != "" && !strings.Contains(err.Error(), wantError)) {
				t.Fatalf("expected %s failure, got %v", scenario, err)
			}
			for _, policyPath := range []string{rt.NFTPath, rt.FallbackNFTPath} {
				b, err := os.ReadFile(policyPath)
				if err != nil {
					t.Fatal(err)
				}
				policy := string(b)
				if !strings.Contains(policy, "blocked_clients { 192.0.2.25 }") || strings.Contains(policy, "proxy_clients { 192.0.2.25 }") {
					t.Fatalf("early error left newly bound client unprotected in %s", policyPath)
				}
				if !strings.Contains(policy, "direct_clients { 192.0.2.26 }") {
					t.Fatal("early failure changed another device's direct policy")
				}
				if strings.Index(policy, "fib daddr type local accept") > strings.Index(policy, "ip saddr @blocked_clients drop") {
					t.Fatal("failed proxy made router management inaccessible")
				}
			}
			if st.Devices[0].Mode != "proxy" || st.Devices[0].NodeID != "selected-node" {
				t.Fatal("temporary guard changed saved binding intent")
			}
		})
	}
}
