package runtime

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"openpass/internal/l2tp"
	"openpass/internal/model"
)

func testL2TPState() model.State {
	st := model.State{Settings: model.DefaultSettings(), Nodes: []model.Node{{ID: "vpn-one", Type: "l2tp", Enabled: true}}, Devices: []model.Device{{IP: "192.0.2.10", MAC: "02:00:00:00:00:01", NodeID: "vpn-one", Mode: "proxy"}}}
	st.Settings.Enabled = true
	return st
}

func TestL2TPRoutePlanFailsClosed(t *testing.T) {
	st := testL2TPState()
	status := map[string]l2tp.Status{"vpn-one": {Connected: true, Device: "unrelated-ppp"}}
	plan := l2tpRoutePlan(st, status)
	if len(plan) != 1 || plan[0].Up || plan[0].Sources[0] != "192.0.2.10/32" {
		t.Fatalf("unexpected plan: %+v", plan)
	}
	status["vpn-one"] = l2tp.Status{Connected: true, Device: l2tp.DeviceName("vpn-one")}
	if !l2tpRoutePlan(st, status)[0].Up {
		t.Fatal("owned PPP was not accepted")
	}
	st.Nodes[0].Enabled = false
	if l2tpRoutePlan(st, status)[0].Up {
		t.Fatal("disabled L2TP must remain unreachable")
	}
	st.Settings.Enabled = false
	if len(l2tpRoutePlan(st, status)) != 0 {
		t.Fatal("global disable must remove policy routing")
	}
}

func TestL2TPRoutesOrderAndRecovery(t *testing.T) {
	var commands []string
	failAdd := true
	r := &l2tpRoutes{path: filepath.Join(t.TempDir(), "routes.json"), run: func(args ...string) ([]byte, error) {
		command := strings.Join(args, " ")
		commands = append(commands, command)
		if strings.HasPrefix(command, "-j ") {
			return []byte("[]"), nil
		}
		if failAdd && strings.Contains(command, "rule add") {
			return []byte("injected failure"), errors.New("failed")
		}
		return nil, nil
	}}
	plan := l2tpRoutePlan(testL2TPState(), nil)
	if err := r.sync(plan); err == nil {
		t.Fatal("expected failure")
	}
	if !r.dirty {
		t.Fatal("partial changes were marked ready")
	}
	failAdd = false
	commands = nil
	if err := r.sync(plan); err != nil {
		t.Fatal(err)
	}
	text := strings.Join(commands, "\n")
	blocked := strings.Index(text, "route replace unreachable default")
	rule := strings.Index(text, "rule add")
	if blocked < 0 || rule < blocked {
		t.Fatal("source rule installed before terminal route")
	}
	if strings.Contains(text, "route replace default dev") {
		t.Fatal("offline tunnel received a live route")
	}
	if r.dirty {
		t.Fatal("successful retry stayed dirty")
	}
	// Loading a journal after process death must recreate rules and throw routes.
	r.loaded = false
	commands = nil
	if err := r.sync(plan); err != nil {
		t.Fatal(err)
	}
	text = strings.Join(commands, "\n")
	if !strings.Contains(text, "rule add") || !strings.Contains(text, "route replace throw 10.0.0.0/8") {
		t.Fatal("restart did not repair partial routing")
	}
	commands = nil
	if err := r.sync(nil); err != nil {
		t.Fatal(err)
	}
	text = strings.Join(commands, "\n")
	if !strings.Contains(text, "rule del") || !strings.Contains(text, "proto 242") {
		t.Fatal("cleanup did not scope owned routes")
	}
	if strings.Contains(text, "table main") || strings.Contains(text, "table 2022") {
		t.Fatal("cleanup touched shared tables")
	}
}

func TestL2TPRoutesRefuseForeignTable(t *testing.T) {
	r := &l2tpRoutes{path: filepath.Join(t.TempDir(), "routes.json"), run: func(args ...string) ([]byte, error) {
		if strings.Join(args[:3], " ") != "-j -4 route" {
			t.Fatal("mutated occupied table")
		}
		return []byte(`[{"dst":"default","dev":"wan"}]`), nil
	}}
	if err := r.sync(l2tpRoutePlan(testL2TPState(), nil)); err == nil {
		t.Fatal("adopted another application's table")
	}
	if _, err := os.Stat(r.path); !os.IsNotExist(err) {
		t.Fatal("claimed a foreign table in journal")
	}
}

func TestL2TPFirewallIsolatesEachPPP(t *testing.T) {
	st := testL2TPState()
	policy := firewallPolicy(st)
	device := l2tp.DeviceName("vpn-one")
	allow := strings.Index(policy, "ip saddr 192.0.2.10 oifname \""+device+"\" accept")
	drop := strings.Index(policy, "ip saddr 192.0.2.10 drop")
	if allow < 0 || drop < allow || drop > strings.Index(policy, "ip saddr @proxy_clients oifname") {
		t.Fatal("PPP guard must precede generic TUN guard")
	}
	if !strings.Contains(policy, "openpass_l2tp_nat ip saddr 192.0.2.10 oifname \""+device+"\" masquerade") {
		t.Fatal("missing narrowly scoped NAT")
	}
	st.Nodes[0].Enabled = false
	policy = firewallPolicy(st)
	if !strings.Contains(policy, "add element inet fw4 blocked_clients { 192.0.2.10 }") || strings.Contains(policy, " masquerade") {
		t.Fatal("disconnected device can escape")
	}
}
