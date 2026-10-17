package l2tp

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"reflect"
	"sort"
	"strings"
	"testing"
	"time"

	"openpass/internal/model"
)

// fakeNetifd models externally observable network state rather than merely
// matching a prerecorded command sequence. ifup is deliberately asynchronous:
// tests explicitly provide the actual PPP device and netifd status.
type fakeNetifd struct {
	config      map[string]map[string]string
	status      map[string]netifdStatus
	devices     map[string]bool
	unavailable map[string]bool
	commands    [][]string
	fail        func(string, []string) bool
	keepUp      bool
}

func newFake() *fakeNetifd {
	return &fakeNetifd{config: map[string]map[string]string{}, status: map[string]netifdStatus{}, devices: map[string]bool{}, unavailable: map[string]bool{}}
}
func (f *fakeNetifd) Available(name string) bool { return !f.unavailable[name] }
func (f *fakeNetifd) Exists(path string) bool {
	if path == "/lib/netifd/proto/l2tp.sh" {
		return !f.unavailable[path]
	}
	return f.devices[strings.TrimPrefix(path, "/sys/class/net/")]
}
func (f *fakeNetifd) Run(ctx context.Context, name string, args ...string) ([]byte, error) {
	if _, ok := ctx.Deadline(); !ok {
		return nil, errors.New("command has no deadline")
	}
	f.commands = append(f.commands, append([]string{name}, args...))
	if f.fail != nil && f.fail(name, args) {
		return []byte("password=sensitive, shell output must not escape"), errors.New("mock secret failure")
	}
	if !f.Available(name) {
		return nil, errors.New("missing command")
	}
	switch name {
	case "uci":
		if args[0] == "-q" {
			args = args[1:]
		}
		switch args[0] {
		case "show":
			var out strings.Builder
			for section, fields := range f.config {
				for key, value := range fields {
					path := "network." + section
					if key != "" {
						path += "." + key
					}
					fmt.Fprintf(&out, "%s='%s'\n", path, strings.ReplaceAll(value, "'", "'\\''"))
				}
			}
			return []byte(out.String()), nil
		case "set":
			key, value, _ := strings.Cut(args[1], "=")
			section, field, _ := strings.Cut(strings.TrimPrefix(key, "network."), ".")
			if f.config[section] == nil {
				f.config[section] = map[string]string{}
			}
			f.config[section][field] = value
		case "delete":
			delete(f.config, strings.TrimPrefix(args[1], "network."))
		case "commit", "revert":
		default:
			return nil, errors.New("unexpected uci operation")
		}
	case "ubus":
		if len(args) == 3 && args[0] == "-S" && args[1] == "list" {
			if _, found := f.status[strings.TrimPrefix(args[2], "network.interface.")]; found {
				return []byte(args[2] + "\n"), nil
			}
			return nil, nil
		}
		if args[1] == "network" && args[2] == "reload" {
			return []byte(`{}`), nil
		}
		if args[1] == "network" && args[2] == "get_proto_handlers" {
			if f.unavailable["l2tp-handler"] {
				return []byte(`{"none":{},"dhcp":{}}`), nil
			}
			return []byte(`{"none":{},"dhcp":{},"l2tp":{"no_device":true}}`), nil
		}
		if args[0] != "call" || args[2] != "status" {
			return nil, errors.New("unexpected ubus operation")
		}
		s, found := f.status[strings.TrimPrefix(args[1], "network.interface.")]
		if !found {
			return nil, errors.New("object absent")
		}
		return json.Marshal(s)
	case "ifup":
		if _, found := f.config[args[0]]; !found {
			return nil, errors.New("no configured interface")
		}
		f.status[args[0]] = netifdStatus{Pending: true, Available: true}
	case "ifdown":
		if !f.keepUp {
			delete(f.status, args[0])
			delete(f.devices, "l2tp-"+args[0])
		}
	default:
		return nil, errors.New("unexpected external program")
	}
	return nil, nil
}

func sampleNode(id string) model.Node {
	return model.Node{ID: id, Type: "l2tp", Name: "Test", Address: "vpn.example.com", Username: "test'user", Password: "test$;password", Enabled: true, MTU: 1400}
}
func (f *fakeNetifd) online(n model.Node) {
	f.devices[DeviceName(n.ID)] = true
	f.status[InterfaceName(n.ID)] = netifdStatus{Up: true, Available: true, L3Device: DeviceName(n.ID), Uptime: 91,
		IPv4: []struct {
			Address string `json:"address"`
		}{{Address: "10.9.0.4"}}}
}
func countCommands(f *fakeNetifd, name string, args ...string) int {
	n := 0
	for _, c := range f.commands {
		if c[0] != name {
			continue
		}
		if len(args) > 0 && !reflect.DeepEqual(c[1:], args) {
			continue
		}
		n++
	}
	return n
}

func TestReconcileCreatesInertIsolatedInterfacesAndRetainsForeignConfig(t *testing.T) {
	f := newFake()
	f.config["wan"] = map[string]string{"": "interface", "proto": "dhcp"}
	f.config["op12345678"] = map[string]string{"": "interface", "proto": "l2tp", "server": "foreign.example.com"}
	beforeWan := map[string]string{"": "interface", "proto": "dhcp"}
	n1, n2 := sampleNode("first"), sampleNode("second")
	n2.Port = 1702
	m := NewWithRunner(f)
	if err := m.Reconcile([]model.Node{n1, n2}, true); err != nil {
		t.Fatal(err)
	}
	if countCommands(f, "ubus", "call", "network", "reload") != 1 || countCommands(f, "uci", "commit", "network") != 1 {
		t.Fatal("multiple network reloads or commits")
	}
	if countCommands(f, "ifup") != 2 {
		t.Fatal("both enabled interfaces should start")
	}
	for _, n := range []model.Node{n1, n2} {
		c := f.config[InterfaceName(n.ID)]
		if c["auto"] != "0" || c["defaultroute"] != "0" || c["peerdns"] != "0" || c["ipv6"] != "0" {
			t.Fatalf("unsafe interface configuration: %+v", c)
		}
		if !isOwned(InterfaceName(n.ID), c) || c["username"] != n.Username || c["password"] != n.Password {
			t.Fatal("credentials or ownership not preserved")
		}
		if len(DeviceName(n.ID)) > 15 {
			t.Fatal("PPP device exceeds Linux limit")
		}
	}
	if !reflect.DeepEqual(f.config["wan"], beforeWan) || f.config["op12345678"]["server"] != "foreign.example.com" {
		t.Fatal("unrelated interface changed")
	}
	if f.config[InterfaceName(n2.ID)]["server"] != "vpn.example.com:1702" {
		t.Fatal("custom L2TP port missing")
	}
}

func TestReconcileNoRestartForUnchangedLiveTunnelAndReconnectsEditedPassword(t *testing.T) {
	f := newFake()
	m := NewWithRunner(f)
	n := sampleNode("first")
	if err := m.Reconcile([]model.Node{n}, true); err != nil {
		t.Fatal(err)
	}
	f.online(n)
	f.commands = nil
	if err := m.Reconcile([]model.Node{n}, true); err != nil {
		t.Fatal(err)
	}
	if countCommands(f, "ifup")+countCommands(f, "ifdown")+countCommands(f, "uci", "commit", "network") != 0 {
		t.Fatal("unchanged connected tunnel was restarted")
	}
	n.Password = "changed'secret"
	if err := m.Reconcile([]model.Node{n}, true); err != nil {
		t.Fatal(err)
	}
	if countCommands(f, "ifdown") != 1 || countCommands(f, "ifup") != 1 {
		t.Fatal("password edit did not reconnect only its own interface")
	}
	if f.config[InterfaceName(n.ID)]["password"] != n.Password {
		t.Fatal("updated password lost")
	}
}

func TestReconcileRestartsPersistedAutoZeroTunnelAfterDaemonRestart(t *testing.T) {
	f := newFake()
	n := sampleNode("persisted")
	f.config[InterfaceName(n.ID)] = configFor(n)
	m := NewWithRunner(f)
	if err := m.Reconcile([]model.Node{n}, true); err != nil {
		t.Fatal(err)
	}
	if countCommands(f, "ifup") != 1 || countCommands(f, "uci", "commit", "network") != 0 {
		t.Fatal("restart should ifup existing auto=0 config without a reload")
	}
	if err := m.Reconcile([]model.Node{n}, true); err != nil {
		t.Fatal(err)
	}
	if countCommands(f, "ifup") != 1 {
		t.Fatal("pending tunnel redialed")
	}
	f.status[InterfaceName(n.ID)] = netifdStatus{}
	m.attempted[n.ID] = time.Now().Add(-time.Minute)
	if err := m.Reconcile([]model.Node{n}, true); err != nil {
		t.Fatal(err)
	}
	if countCommands(f, "ifup") != 2 {
		t.Fatal("failed tunnel not retried after backoff")
	}
}

func TestPersistentPendingTimesOutAndRetriesOnlyItsOwnInterface(t *testing.T) {
	f := newFake()
	n, healthy := sampleNode("stuck"), sampleNode("healthy")
	m := NewWithRunner(f)
	if err := m.Reconcile([]model.Node{n, healthy}, true); err != nil {
		t.Fatal(err)
	}
	f.online(healthy)
	f.commands = nil
	m.pendingSince[n.ID] = time.Now().Add(-connectionTimeout - time.Second)
	if s := m.Status(n.ID); s.State != "error" || !strings.Contains(s.LastError, "等待自动重试") {
		t.Fatalf("expired pending connection still presented as ordinary connecting: %+v", s)
	}
	if err := m.Reconcile([]model.Node{n, healthy}, true); err != nil {
		t.Fatal(err)
	}
	if countCommands(f, "ifdown", InterfaceName(n.ID)) != 1 || countCommands(f, "ifup", InterfaceName(n.ID)) != 1 {
		t.Fatal("persistent pending tunnel was not disconnected and redialed")
	}
	if countCommands(f, "ifdown", InterfaceName(healthy.ID))+countCommands(f, "ifup", InterfaceName(healthy.ID)) != 0 || countCommands(f, "ubus", "call", "network", "reload") != 0 {
		t.Fatal("pending retry affected a healthy interface or reloaded the network")
	}
	if s := m.Status(n.ID); s.State != "connecting" || !strings.Contains(s.LastError, "已重新拨号") {
		t.Fatalf("successful retry should show connecting and explain the prior timeout: %+v", s)
	}
	if err := m.Reconcile([]model.Node{n, healthy}, true); err != nil {
		t.Fatal(err)
	}
	if countCommands(f, "ifup", InterfaceName(n.ID)) != 1 {
		t.Fatal("pending retry ignored backoff")
	}
	f.online(n)
	if s := m.Status(n.ID); !s.Connected || s.LastError != "" {
		t.Fatalf("connected tunnel retained timeout warning: %+v", s)
	}
}

func TestInheritedPendingGetsFiniteGracePeriod(t *testing.T) {
	f := newFake()
	n := sampleNode("inherited-pending")
	f.config[InterfaceName(n.ID)] = configFor(n)
	f.status[InterfaceName(n.ID)] = netifdStatus{Pending: true, Available: true}
	m := NewWithRunner(f)
	if err := m.Reconcile([]model.Node{n}, true); err != nil {
		t.Fatal(err)
	}
	if m.pendingSince[n.ID].IsZero() || countCommands(f, "ifdown") != 0 || countCommands(f, "ifup") != 0 {
		t.Fatal("inherited pending connection needs a tracked grace period before interference")
	}
	m.pendingSince[n.ID] = time.Now().Add(-connectionTimeout - time.Second)
	if err := m.Reconcile([]model.Node{n}, true); err != nil {
		t.Fatal(err)
	}
	if countCommands(f, "ifdown") != 1 || countCommands(f, "ifup") != 1 {
		t.Fatal("inherited pending connection remained permanently stuck")
	}
}

func TestTimedOutPendingDisconnectFailureIsVisibleAndRateLimited(t *testing.T) {
	f := newFake()
	n := sampleNode("stuck-down-failure")
	m := NewWithRunner(f)
	if err := m.Reconcile([]model.Node{n}, true); err != nil {
		t.Fatal(err)
	}
	m.pendingSince[n.ID] = time.Now().Add(-connectionTimeout - time.Second)
	f.commands = nil
	f.fail = func(name string, args []string) bool { return name == "ifdown" }
	if err := m.Reconcile([]model.Node{n}, true); err == nil || !strings.Contains(err.Error(), "连接超时") {
		t.Fatal("failed pending timeout recovery not reported")
	}
	if s := m.Status(n.ID); s.State != "error" || !strings.Contains(s.LastError, "断开重试失败") {
		t.Fatalf("missing actionable retry failure: %+v", s)
	}
	if err := m.Reconcile([]model.Node{n}, true); err != nil {
		t.Fatal(err)
	}
	if countCommands(f, "ifdown") != 1 || countCommands(f, "ifup") != 0 {
		t.Fatal("failed disconnect was retried too quickly or followed by an unsafe duplicate connection")
	}
}

func TestDisableStopsOwnedTunnelsWithoutRemovingConfig(t *testing.T) {
	f := newFake()
	n := sampleNode("first")
	f.config[InterfaceName(n.ID)] = configFor(n)
	f.online(n)
	m := NewWithRunner(f)
	if err := m.Reconcile([]model.Node{n}, false); err != nil {
		t.Fatal(err)
	}
	if countCommands(f, "ifdown") != 1 || countCommands(f, "ifup") != 0 {
		t.Fatal("disable must only stop")
	}
	if f.config[InterfaceName(n.ID)]["auto"] != "0" {
		t.Fatal("disabled interface must remain inert on boot")
	}
	if err := m.Reconnect(n.ID); err == nil {
		t.Fatal("disabled service accepted reconnect")
	}
}

func TestRemovalCleansStaleOwnedInterfacesOnly(t *testing.T) {
	f := newFake()
	n := sampleNode("stale")
	f.config[InterfaceName(n.ID)] = configFor(n)
	f.online(n)
	f.config["op12345678"] = map[string]string{"": "interface", "proto": "l2tp", "openpass_owned": "1", "openpass_node": "mismatched-id"}
	if err := NewWithRunner(f).Reconcile(nil, true); err != nil {
		t.Fatal(err)
	}
	if _, ok := f.config[InterfaceName(n.ID)]; ok {
		t.Fatal("stale owned interface left behind")
	}
	if _, ok := f.config["op12345678"]; !ok {
		t.Fatal("foreign lookalike removed")
	}
	if countCommands(f, "ifdown") != 1 {
		t.Fatal("only owned interface should be stopped")
	}
}

func TestNameCollisionRejectedBeforeMutation(t *testing.T) {
	f := newFake()
	n := sampleNode("first")
	f.config[InterfaceName(n.ID)] = map[string]string{"": "interface", "proto": "dhcp"}
	if err := NewWithRunner(f).Reconcile([]model.Node{n}, true); err == nil {
		t.Fatal("name collision accepted")
	}
	if countCommands(f, "ifdown")+countCommands(f, "ifup") != 0 || f.config[InterfaceName(n.ID)]["proto"] != "dhcp" {
		t.Fatal("foreign interface mutated")
	}
}

func TestMissingDependenciesDoesNotAffectOrdinaryNodes(t *testing.T) {
	f := newFake()
	f.unavailable["uci"] = true
	f.unavailable["xl2tpd"] = true
	m := NewWithRunner(f)
	if err := m.Reconcile([]model.Node{{ID: "normal", Type: "socks"}}, true); err != nil {
		t.Fatal(err)
	}
	if len(f.commands) != 0 {
		t.Fatal("ordinary nodes should not require OpenWrt commands")
	}
	n := sampleNode("first")
	if err := m.Reconcile([]model.Node{n}, true); err == nil {
		t.Fatal("missing L2TP dependencies not reported")
	}
	if status := m.Status(n.ID); status.State != "error" || !strings.Contains(status.LastError, "xl2tpd") {
		t.Fatalf("missing dependency not exposed: %+v", status)
	}
}

func TestDisabledL2TPNeedsNoOptionalDependenciesOrUCIWrites(t *testing.T) {
	for _, missingUCI := range []bool{false, true} {
		f := newFake()
		f.unavailable["xl2tpd"] = true
		f.unavailable["uci"] = missingUCI
		n := sampleNode("saved-disabled")
		n.Enabled = false
		if err := NewWithRunner(f).Reconcile([]model.Node{n, {ID: "ordinary", Type: "socks", Enabled: true}}, true); err != nil {
			t.Fatalf("disabled optional protocol blocked normal proxy: %v", err)
		}
		if len(f.config) != 0 || countCommands(f, "ifup") != 0 || countCommands(f, "uci", "commit", "network") != 0 {
			t.Fatal("saving disabled L2TP should not create UCI configuration without its dependencies")
		}
	}
}

func TestNewlyInstalledProtocolMustBeRegisteredByRunningNetifd(t *testing.T) {
	f := newFake()
	f.unavailable["l2tp-handler"] = true
	m := NewWithRunner(f)
	d := m.Dependencies()
	if d.Ready || len(d.Missing) != 1 || !strings.Contains(d.Missing[0], "netifd 尚未加载 L2TP") {
		t.Fatalf("installed but inactive protocol was not explained: %+v", d)
	}
	n := sampleNode("new-package")
	err := m.Reconcile([]model.Node{n}, true)
	if err == nil || !strings.Contains(err.Error(), "重启路由器或网络服务") {
		t.Fatalf("missing live handler should require manual network reload: %v", err)
	}
	if len(f.config) != 0 || countCommands(f, "ifup") != 0 || countCommands(f, "ubus", "call", "network", "reload") != 0 {
		t.Fatal("inactive protocol caused an interface mutation or network restart")
	}
	// After an administrator restarts netifd, the next check discovers the
	// handler without restarting OpenPass or caching the previous failure.
	f.unavailable["l2tp-handler"] = false
	if !m.Dependencies().Ready {
		t.Fatal("protocol readiness was cached after netifd loaded it")
	}
	if err := m.Reconcile([]model.Node{n}, true); err != nil {
		t.Fatal(err)
	}
	if countCommands(f, "ifup") != 1 {
		t.Fatal("registered protocol was not started")
	}
}

func TestUnreachableNetifdCannotPassDependencyCheck(t *testing.T) {
	f := newFake()
	f.fail = func(name string, args []string) bool {
		return name == "ubus" && len(args) == 3 && args[2] == "get_proto_handlers"
	}
	d := NewWithRunner(f).Dependencies()
	if d.Ready || len(d.Missing) != 1 || !strings.Contains(d.Missing[0], "无法读取 netifd") || strings.Contains(d.Missing[0], "sensitive") {
		t.Fatalf("netifd readiness failure missing or leaked command output: %+v", d)
	}
}

func TestStatusUsesActualPPPInterfaceAndSanitizesNetifdErrors(t *testing.T) {
	f := newFake()
	n := sampleNode("first")
	m := NewWithRunner(f)
	f.online(n)
	s := m.Status(n.ID)
	if !s.Connected || s.Device != DeviceName(n.ID) || s.LocalIP != "10.9.0.4" || s.Uptime != 91 {
		t.Fatalf("bad online state: %+v", s)
	}
	raw := f.status[InterfaceName(n.ID)]
	raw.L3Device = "eth0"
	f.status[InterfaceName(n.ID)] = raw
	if s = m.Status(n.ID); s.Connected || s.State != "error" || s.Device != "" {
		t.Fatalf("foreign l3_device accepted: %+v", s)
	}
	f.status[InterfaceName(n.ID)] = netifdStatus{Errors: []struct {
		Code string `json:"code"`
	}{{Code: "password=secret"}}}
	if s = m.Status(n.ID); s.State != "error" || strings.Contains(s.LastError, "secret") {
		t.Fatalf("netifd leaked arbitrary diagnostic output: %+v", s)
	}
	f.status[InterfaceName(n.ID)] = netifdStatus{Errors: []struct {
		Code string `json:"code"`
	}{{Code: "AUTH_FAILED"}}}
	if s = m.Status(n.ID); !strings.Contains(s.LastError, "认证失败") {
		t.Fatalf("authentication failure not clear: %+v", s)
	}
}

func TestUninstallCleanupStopsOnlyOwnedInterfaceAndPreservesFailure(t *testing.T) {
	f := newFake()
	n := sampleNode("first")
	f.config[InterfaceName(n.ID)] = configFor(n)
	f.online(n)
	f.fail = func(name string, args []string) bool { return name == "ifdown" }
	if err := NewWithRunner(f).Cleanup(true); err == nil {
		t.Fatal("failed disconnect considered successful")
	}
	if _, ok := f.config[InterfaceName(n.ID)]; !ok {
		t.Fatal("configuration removed while tunnel still online")
	}
	f.fail = nil
	if err := NewWithRunner(f).Cleanup(true); err != nil {
		t.Fatal(err)
	}
	if len(f.config) != 0 || len(f.devices) != 0 {
		t.Fatal("cleanup left owned interface/configuration")
	}
}

func TestCleanupCannotAssumeOfflineWhenBothIfdownAndUbusFail(t *testing.T) {
	f := newFake()
	n := sampleNode("pending")
	f.config[InterfaceName(n.ID)] = configFor(n)
	f.fail = func(name string, args []string) bool { return name == "ifdown" || name == "ubus" }
	if err := NewWithRunner(f).Cleanup(true); err == nil {
		t.Fatal("unreachable ubus incorrectly proves no pending tunnel")
	}
	if _, ok := f.config[InterfaceName(n.ID)]; !ok {
		t.Fatal("pending interface configuration removed without a verified disconnect")
	}
}

func TestCredentialValidationAndErrorRedaction(t *testing.T) {
	for _, value := range []string{"bad\noption", "bad\"option", "bad\\option", "bad\x00option"} {
		n := sampleNode("first")
		n.Password = value
		if err := Validate(n); err == nil || strings.Contains(err.Error(), value) {
			t.Fatal("unsafe credential accepted or exposed")
		}
	}
	f := newFake()
	n := sampleNode("first")
	f.fail = func(name string, args []string) bool {
		return name == "uci" && args[0] == "set" && strings.Contains(args[1], ".password=")
	}
	err := NewWithRunner(f).Reconcile([]model.Node{n}, true)
	if err == nil || strings.Contains(err.Error(), "sensitive") || strings.Contains(err.Error(), n.Password) {
		t.Fatal("credential-bearing command error leaked")
	}
	if countCommands(f, "ifup") != 0 || countCommands(f, "uci", "commit", "network") != 0 {
		t.Fatal("partial configuration started or committed")
	}
}

func TestUCIQuotingAndInterfaceNaming(t *testing.T) {
	n := sampleNode("round-trip")
	f := newFake()
	f.config[InterfaceName(n.ID)] = configFor(n)
	data, _ := f.Run(contextWithDeadline(t), "uci", "-q", "show", "network")
	if got := parseNetwork(data)[InterfaceName(n.ID)]; !reflect.DeepEqual(got, configFor(n)) {
		t.Fatal("UCI quoted credentials did not round-trip")
	}
	names := []string{InterfaceName("first"), InterfaceName("second"), InterfaceName("third")}
	sort.Strings(names)
	if names[0] == names[1] || names[1] == names[2] {
		t.Fatal("interface names collided")
	}
	if DeviceName("../../wan") != DeviceName("../../wan") || !ownedName.MatchString(InterfaceName("../../wan")) {
		t.Fatal("untrusted ID escaped the interface namespace")
	}
}
func contextWithDeadline(t *testing.T) context.Context {
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	t.Cleanup(cancel)
	return ctx
}
