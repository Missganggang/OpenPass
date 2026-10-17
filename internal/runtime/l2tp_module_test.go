package runtime

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"openpass/internal/l2tp"
	"openpass/internal/model"
)

type moduleRunner struct {
	mu       sync.Mutex
	commands []string
	checks   []string
	missing  bool
	owned    string
	failRead bool
}

func (r *moduleRunner) Run(_ context.Context, name string, args ...string) ([]byte, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	command := name + " " + strings.Join(args, " ")
	r.commands = append(r.commands, command)
	switch command {
	case "uci -q show network":
		if r.failRead {
			return nil, fmt.Errorf("injected UCI read failure")
		}
		if r.owned != "" {
			name := l2tp.InterfaceName(r.owned)
			return []byte(fmt.Sprintf("network.%s=interface\nnetwork.%s.proto='l2tp'\nnetwork.%s.openpass_owned='1'\nnetwork.%s.openpass_node='%s'\nnetwork.foreign=interface\nnetwork.foreign.proto='l2tp'\n", name, name, name, name, r.owned)), nil
		}
		return nil, nil
	case "ubus call network get_proto_handlers":
		return []byte(`{"l2tp":{}}`), nil
	}
	if strings.HasSuffix(command, " status") {
		return []byte(`{"up":false,"pending":false}`), nil
	}
	return nil, nil
}

func (r *moduleRunner) Available(name string) bool {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.checks = append(r.checks, name)
	return !r.missing || name == "uci" || name == "ubus" || name == "ifdown"
}

func (r *moduleRunner) Exists(path string) bool { return path == "/lib/netifd/proto/l2tp.sh" }

func (r *moduleRunner) snapshot() string {
	r.mu.Lock()
	defer r.mu.Unlock()
	return strings.Join(r.commands, "\n") + "\nchecks:" + strings.Join(r.checks, ",")
}

func moduleRuntime(t *testing.T, runner *moduleRunner) *Runtime {
	t.Helper()
	return &Runtime{
		l2tpManager: l2tp.NewWithRunner(runner),
		l2tpRoutes:  &l2tpRoutes{path: filepath.Join(t.TempDir(), "routes.json"), run: func(...string) ([]byte, error) { return []byte("[]"), nil }},
	}
}

func TestL2TPModuleDisableStopsOnlyOwnedInterfacesWithoutDependencies(t *testing.T) {
	runner := &moduleRunner{owned: "vpn-one", missing: true}
	rt := moduleRuntime(t, runner)
	st := testL2TPState()
	st.Settings.L2TPDisabled = true
	stop := make(chan struct{})
	rt.l2tpStop = stop
	if err := rt.configureL2TPLocked(st); err != nil {
		t.Fatal(err)
	}
	select {
	case <-stop:
	default:
		t.Fatal("L2TP polling was not cancelled")
	}
	if rt.l2tpStop != nil {
		t.Fatal("saved nodes restarted the disabled module's watcher")
	}
	if !st.Nodes[0].Enabled {
		t.Fatal("module shutdown lost saved per-node preferences")
	}
	commands := runner.snapshot()
	if !strings.Contains(commands, "ifdown "+l2tp.InterfaceName("vpn-one")) || !strings.Contains(commands, "uci -q delete network."+l2tp.InterfaceName("vpn-one")) {
		t.Fatal("owned L2TP interface remained configured")
	}
	for _, forbidden := range []string{"ifdown foreign", "kill", "get_proto_handlers", "checks:uci,pppd", "ifup "} {
		if strings.Contains(commands, forbidden) {
			t.Fatalf("shutdown performed unrelated work: %s", forbidden)
		}
	}
	before := runner.snapshot()
	if err := rt.validateL2TP(st); err != nil {
		t.Fatal(err)
	}
	statuses := rt.L2TPStatuses(st.Nodes)
	if statuses["vpn-one"].State != "disabled" || statuses["vpn-one"].Connected {
		t.Fatalf("disabled module reported a live tunnel: %+v", statuses)
	}
	if err := rt.ReconnectL2TP("vpn-one"); err == nil {
		t.Fatal("disabled module allowed reconnection")
	}
	if err := rt.refreshL2TPRoutesLocked(); err != nil {
		t.Fatal(err)
	}
	if runner.snapshot() != before {
		t.Fatal("disabled module queried dependencies or live status")
	}
}

func TestL2TPModuleReenableRestoresSavedConnectionPreference(t *testing.T) {
	runner := &moduleRunner{}
	rt := moduleRuntime(t, runner)
	st := testL2TPState()
	st.Nodes[0].Address, st.Nodes[0].Username, st.Nodes[0].Password = "l2tp.example.test", "test-user", "test-password"
	st.Nodes[0].Port = 1701
	st.Nodes = append(st.Nodes, model.Node{ID: "paused", Type: "l2tp", Address: "paused.example.test", Username: "test-user", Password: "test-password", Port: 1701})
	st.Settings.L2TPDisabled = true
	if err := rt.configureL2TPLocked(st); err != nil {
		t.Fatal(err)
	}
	st.Settings.L2TPDisabled = false
	if err := rt.configureL2TPLocked(st); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		rt.mu.Lock()
		defer rt.mu.Unlock()
		rt.stopL2TPWatcherLocked()
	})
	if rt.l2tpStop == nil {
		t.Fatal("reenabling did not restore connection monitoring")
	}
	commands := runner.snapshot()
	if !strings.Contains(commands, "ifup "+l2tp.InterfaceName("vpn-one")) {
		t.Fatal("saved enabled connection was not restarted")
	}
	if strings.Contains(commands, "ifup "+l2tp.InterfaceName("paused")) {
		t.Fatal("module enable ignored a saved disconnected connection")
	}
}

func TestL2TPModuleDisableStillStopsWhenFirewallWriteFails(t *testing.T) {
	runner := &moduleRunner{owned: "vpn-one", missing: true}
	rt := moduleRuntime(t, runner)
	badDirectory := filepath.Join(t.TempDir(), "not-a-directory")
	if err := os.WriteFile(badDirectory, []byte("occupied"), 0600); err != nil {
		t.Fatal(err)
	}
	rt.NFTPath = filepath.Join(badDirectory, "policy.nft")
	stop := make(chan struct{})
	rt.l2tpStop = stop
	st := testL2TPState()
	st.Settings.L2TPDisabled = true
	if err := rt.Apply(st); err == nil {
		t.Fatal("firewall failure was hidden")
	} else if rt.L2TPModuleError() != err.Error() {
		t.Fatal("firewall failure was not retained for subsequent status reads")
	}
	select {
	case <-stop:
	default:
		t.Fatal("firewall failure left L2TP polling running")
	}
	if rt.l2tpStop != nil || !rt.l2tpState.Settings.L2TPDisabled {
		t.Fatal("failed firewall write left module active")
	}
	if !strings.Contains(runner.snapshot(), "ifdown "+l2tp.InterfaceName("vpn-one")) {
		t.Fatal("firewall failure skipped owned tunnel shutdown")
	}
}

func TestL2TPModuleErrorRetainsStartupCleanupFailure(t *testing.T) {
	runner := &moduleRunner{failRead: true}
	rt := moduleRuntime(t, runner)
	st := model.State{Settings: model.DefaultSettings()}
	st.Settings.Enabled, st.Settings.L2TPDisabled = false, true
	err := rt.Apply(st)
	if err == nil || !strings.Contains(err.Error(), "无法读取 OpenWrt 网络配置") {
		t.Fatalf("expected cleanup failure, got %v", err)
	}
	before := runner.snapshot()
	for i := 0; i < 3; i++ {
		if rt.L2TPModuleError() != err.Error() {
			t.Fatal("cleanup failure was not retained")
		}
	}
	if before != runner.snapshot() {
		t.Fatal("reading the retained error queried the system")
	}
}

func TestL2TPModuleErrorClearsOnSuccessfulApply(t *testing.T) {
	t.Setenv("PATH", t.TempDir()) // No real router commands in this lifecycle test.
	for _, disabled := range []bool{true, false} {
		t.Run(fmt.Sprintf("disabled=%t", disabled), func(t *testing.T) {
			rt := moduleRuntime(t, &moduleRunner{})
			rt.l2tpModuleError = "previous failed apply"
			rt.ConfigPath = filepath.Join(t.TempDir(), "core.json")
			rt.NFTPath = filepath.Join(t.TempDir(), "policy.nft")
			rt.SingBoxPath = "missing-sing-box-test-command"
			st := model.State{Settings: model.DefaultSettings()}
			st.Settings.Enabled, st.Settings.L2TPDisabled = false, disabled
			if err := rt.Apply(st); err != nil {
				t.Fatal(err)
			}
			if rt.L2TPModuleError() != "" {
				t.Fatal("successful apply did not clear the previous failure")
			}
		})
	}
}

func TestL2TPStoppedWatcherDoesNoWork(t *testing.T) {
	runner := &moduleRunner{}
	rt := moduleRuntime(t, runner)
	rt.l2tpState = testL2TPState()
	stop, done := make(chan struct{}), make(chan struct{})
	close(stop)
	go func() { rt.watchL2TP(stop); close(done) }()
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("cancelled watcher remained running")
	}
	if runner.snapshot() != "\nchecks:" {
		t.Fatal("cancelled watcher queried netifd")
	}
}

func TestL2TPDisabledModulePreservesOrdinaryProxyAndManagement(t *testing.T) {
	st := testL2TPState()
	st.Settings.L2TPDisabled = true
	st.Nodes = append(st.Nodes, model.Node{ID: "socks", Type: "socks", Enabled: true})
	st.Devices = append(st.Devices, model.Device{IP: "192.0.2.11", Mode: "proxy", NodeID: "socks"}, model.Device{IP: "192.0.2.12", Mode: "direct"})
	policy := firewallPolicy(st)
	for _, needed := range []string{"add element inet fw4 blocked_clients { 192.0.2.10 }", "add element inet fw4 proxy_clients { 192.0.2.11 }", "add element inet fw4 direct_clients { 192.0.2.12 }"} {
		if !strings.Contains(policy, needed) {
			t.Fatalf("missing policy %q", needed)
		}
	}
	if strings.Contains(policy, " masquerade") || strings.Contains(policy, "oifname \"l2tp-") {
		t.Fatal("disabled tunnel retains forwarding permission")
	}
	if strings.Index(policy, "fib daddr type local accept") > strings.Index(policy, "ip saddr @blocked_clients drop") {
		t.Fatal("disabled clients lost router management access")
	}
	if plan := l2tpRoutePlan(st, map[string]l2tp.Status{"vpn-one": {Connected: true, Device: l2tp.DeviceName("vpn-one")}}); len(plan) != 0 {
		t.Fatal("disabled module retains L2TP policy routes")
	}
}
