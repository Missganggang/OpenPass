package runtime

import (
	"io"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	goruntime "runtime"
	"strings"
	"syscall"
	"testing"
	"time"

	"openpass/internal/model"
)

func TestReadinessWaitsForFinalStartupMessage(t *testing.T) {
	l := &coreStartupLog{writer: io.Discard, ready: make(chan struct{})}
	_, _ = l.Write([]byte("tcp server started at 0.0.0.0:1053\n"))
	select {
	case <-l.ready:
		t.Fatal("released traffic before post-start completed")
	default:
	}
	_, _ = l.Write([]byte("INFO sing-box sta"))
	select {
	case <-l.ready:
		t.Fatal("released traffic on partial message")
	default:
	}
	_, _ = l.Write([]byte("rted (1.25s)\n"))
	select {
	case <-l.ready:
	default:
		t.Fatal("final startup message missed")
	}
}

// A child must receive SIGTERM and finish cleanup before Stop returns.
// Killing sing-box without cleanup can strand the router's policy routes.
func TestGracefulShutdownHelper(t *testing.T) {
	if os.Getenv("OPENPASS_SHUTDOWN_HELPER") != "1" {
		return
	}
	marker := os.Getenv("OPENPASS_SHUTDOWN_MARKER")
	ch := make(chan os.Signal, 1)
	signal.Notify(ch, syscall.SIGTERM)
	_ = os.WriteFile(marker+".ready", []byte("ready"), 0600)
	<-ch
	_ = os.WriteFile(marker, []byte("cleaned"), 0600)
	os.Exit(0)
}

func TestStopWaitsForGracefulCleanup(t *testing.T) {
	if goruntime.GOOS == "windows" {
		t.Skip("router process signals require Linux")
	}
	marker := filepath.Join(t.TempDir(), "cleanup")
	executable, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	cmd := exec.Command(executable, "-test.run=^TestGracefulShutdownHelper$")
	cmd.Env = append(os.Environ(), "OPENPASS_SHUTDOWN_HELPER=1", "OPENPASS_SHUTDOWN_MARKER="+marker)
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	rt := &Runtime{cmd: cmd, done: make(chan struct{})}
	go rt.monitor(cmd, model.State{}, rt.done)
	t.Cleanup(func() { rt.Stop() })
	deadline := time.Now().Add(3 * time.Second)
	for {
		if _, err := os.Stat(marker + ".ready"); err == nil {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("child did not start")
		}
		time.Sleep(10 * time.Millisecond)
	}
	rt.Stop()
	content, err := os.ReadFile(marker)
	if err != nil || string(content) != "cleaned" {
		t.Fatalf("cleanup did not complete before stop: %s %v", content, err)
	}
}

func TestFirewallKeepsManagementReachableWhenProxyFails(t *testing.T) {
	// Exclude the real nft executable: inspect policy output without changing
	// the machine's networking.
	t.Setenv("PATH", t.TempDir())
	rt := &Runtime{NFTPath: filepath.Join(t.TempDir(), "policy.nft")}
	st := model.State{Settings: model.DefaultSettings(), Devices: []model.Device{{IP: "192.0.2.25", Mode: "blocked"}}}
	st.Settings.Enabled = true
	if err := rt.writeFirewall(st); err != nil {
		t.Fatal(err)
	}
	b, err := os.ReadFile(rt.NFTPath)
	if err != nil {
		t.Fatal(err)
	}
	policy := string(b)
	allow := strings.Index(policy, "fib daddr type local accept")
	block := strings.Index(policy, "ip saddr @blocked_clients drop")
	if allow < 0 || block < 0 || allow > block {
		t.Fatal("management exception must precede failed-proxy block")
	}
	if !strings.Contains(policy, "udp dport 53 redirect to :1053") {
		t.Fatal("management exception must retain DNS enforcement")
	}
	if strings.Contains(policy, "ip saddr != 0.0.0.0 drop") {
		t.Fatal("new devices must honor default direct")
	}
	st.Settings.DefaultMode = "blocked"
	if err := rt.writeFirewall(st); err != nil {
		t.Fatal(err)
	}
	b, _ = os.ReadFile(rt.NFTPath)
	if !strings.Contains(string(b), "ip saddr != 0.0.0.0 drop") {
		t.Fatal("blocked default lost")
	}
}

func TestReloadPolicyHasFailClosedBootFallback(t *testing.T) {
	t.Setenv("PATH", t.TempDir())
	dir := t.TempDir()
	rt := &Runtime{NFTPath: filepath.Join(dir, "run", "policy.nft"), FallbackNFTPath: filepath.Join(dir, "etc", "policy.nft")}
	st := model.State{Settings: model.DefaultSettings(), Devices: []model.Device{{IP: "192.0.2.25", Mode: "proxy"}}}
	st.Settings.Enabled = true
	if err := rt.writeFirewall(st); err != nil {
		t.Fatal(err)
	}
	live, _ := os.ReadFile(rt.NFTPath)
	boot, _ := os.ReadFile(rt.FallbackNFTPath)
	if !strings.Contains(string(live), "add element inet fw4 proxy_clients { 192.0.2.25 }") || !strings.Contains(string(boot), "add element inet fw4 blocked_clients { 192.0.2.25 }") {
		t.Fatal("reload must restore live bindings; boot must block until the core is ready")
	}
	if !strings.Contains(string(live), "ip saddr @proxy_clients oifname != \"openpass0\" drop") {
		t.Fatal("proxy clients could escape via WAN after core failure")
	}
	if st.Devices[0].Mode != "proxy" {
		t.Fatal("fallback mutated the saved device binding")
	}
	st.Settings.Enabled = false
	if err := rt.writeFirewall(st); err != nil {
		t.Fatal(err)
	}
	boot, _ = os.ReadFile(rt.FallbackNFTPath)
	if strings.Contains(string(boot), "add rule") {
		t.Fatal("disabled protection persisted active rules")
	}
}
