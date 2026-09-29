package runtime

// Package runtime contains the small amount of OpenWrt integration that must
// stay outside the HTTP layer: writing a sing-box config, keeping the
// fail-closed client sets current, and supervising the sing-box process.

import (
	"encoding/json"
	"fmt"
	"io"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"syscall"
	"time"

	"openpass/internal/model"
	"openpass/internal/singbox"
)

type Runtime struct {
	SingBoxPath     string
	ConfigPath      string
	NFTPath         string
	FallbackNFTPath string

	mu   sync.Mutex
	cmd  *exec.Cmd
	done chan struct{}
}

type coreStartupLog struct {
	mu     sync.Mutex
	writer io.Writer
	ready  chan struct{}
	window string
	once   sync.Once
}

func (l *coreStartupLog) Write(p []byte) (int, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.window += string(p)
	if strings.Contains(l.window, "sing-box started") {
		l.once.Do(func() { close(l.ready) })
	}
	if len(l.window) > 2048 {
		l.window = l.window[len(l.window)-2048:]
	}
	return l.writer.Write(p)
}

func New(singBoxPath, configPath string) *Runtime {
	if singBoxPath == "" {
		singBoxPath = "/usr/bin/sing-box"
	}
	if configPath == "" {
		configPath = "/var/run/openpass/sing-box.json"
	}
	return &Runtime{SingBoxPath: singBoxPath, ConfigPath: configPath, NFTPath: "/var/run/openpass/91-openpass-dynamic.nft", FallbackNFTPath: "/etc/openpass/firewall-policy.nft"}
}

// Apply writes the configuration atomically, validates it when sing-box is
// installed, refreshes the client policy sets, and starts the kernel only when
// the user explicitly enabled protection.
func (rt *Runtime) Apply(st model.State) error {
	rt.mu.Lock()
	defer rt.mu.Unlock()
	b, err := singbox.Build(st)
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(rt.ConfigPath), 0755); err != nil {
		return err
	}
	tmp := rt.ConfigPath + ".tmp"
	if err := os.WriteFile(tmp, b, 0600); err != nil {
		return err
	}
	if err := os.Rename(tmp, rt.ConfigPath); err != nil {
		return err
	}
	if _, err := exec.LookPath(rt.SingBoxPath); err == nil {
		check := exec.Command(rt.SingBoxPath, "check", "-c", rt.ConfigPath)
		if output, checkErr := check.CombinedOutput(); checkErr != nil {
			return fmt.Errorf("sing-box config check failed: %s: %w", strings.TrimSpace(string(output)), checkErr)
		}
	}
	transition := st
	if st.Settings.Enabled {
		transition.Devices = append([]model.Device(nil), st.Devices...)
		for i := range transition.Devices {
			if transition.Devices[i].Mode == "proxy" {
				transition.Devices[i].Mode = "blocked"
			}
		}
	}
	if err := rt.writeFirewall(transition); err != nil {
		return err
	}
	rt.stopLocked()
	if !st.Settings.Enabled {
		return nil
	}
	if _, err := exec.LookPath(rt.SingBoxPath); err != nil {
		return fmt.Errorf("sing-box not found at %s", rt.SingBoxPath)
	}
	rt.cmd = exec.Command(rt.SingBoxPath, "run", "-c", rt.ConfigPath)
	startupLog := &coreStartupLog{writer: os.Stderr, ready: make(chan struct{})}
	rt.cmd.Stdout, rt.cmd.Stderr = startupLog, startupLog
	cmd := rt.cmd
	if err := cmd.Start(); err != nil {
		rt.cmd = nil
		failed := st
		failed.Devices = append([]model.Device(nil), st.Devices...)
		for i := range failed.Devices {
			if failed.Devices[i].Mode == "proxy" {
				failed.Devices[i].Mode = "blocked"
			}
		}
		_ = rt.writeFirewall(failed)
		return fmt.Errorf("start sing-box: %w", err)
	}
	rt.done = make(chan struct{})
	go rt.monitor(cmd, st, rt.done)
	// Opening DNS happens before sing-box's post-start firewall setup. Wait
	// for its final startup message before releasing proxy traffic.
	select {
	case <-rt.done:
		_ = rt.writeFirewall(transition)
		return fmt.Errorf("sing-box exited during startup; proxy clients remain blocked")
	case <-time.After(12 * time.Second):
		rt.stopLocked()
		_ = rt.writeFirewall(transition)
		return fmt.Errorf("sing-box startup timed out; proxy clients remain blocked")
	case <-startupLog.ready:
		if err := rt.writeFirewall(st); err != nil {
			rt.stopLocked()
			return err
		}
		return nil
	}
}

func (rt *Runtime) Stop() {
	rt.mu.Lock()
	defer rt.mu.Unlock()
	rt.stopLocked()
}

func (rt *Runtime) Running() bool {
	rt.mu.Lock()
	defer rt.mu.Unlock()
	if rt.cmd == nil || rt.done == nil {
		return false
	}
	select {
	case <-rt.done:
		return false
	default:
		return true
	}
}

func (rt *Runtime) stopLocked() {
	if rt.cmd != nil && rt.cmd.Process != nil {
		// sing-box must handle SIGTERM to remove its TUN policy routes and
		// auto_redirect rules. SIGKILL leaves rules that can lock out clients.
		_ = rt.cmd.Process.Signal(syscall.SIGTERM)
		if rt.done != nil {
			select {
			case <-rt.done:
			case <-time.After(5 * time.Second):
				_ = rt.cmd.Process.Kill()
				<-rt.done
			}
		}
	}
	rt.cmd = nil
	rt.done = nil
}

func (rt *Runtime) monitor(cmd *exec.Cmd, st model.State, done chan struct{}) {
	_ = cmd.Wait()
	close(done)
	rt.mu.Lock()
	defer rt.mu.Unlock()
	if rt.cmd != cmd {
		return
	}
	rt.cmd = nil
	if !st.Settings.Enabled {
		return
	}
	// A dead kernel must never turn a proxy device into a direct device. Keep
	// the firewall policy in its fail-closed form until the next Apply call.
	failed := st
	failed.Devices = append([]model.Device(nil), st.Devices...)
	for i := range failed.Devices {
		if failed.Devices[i].Mode == "proxy" {
			failed.Devices[i].Mode = "blocked"
		}
	}
	_ = rt.writeFirewall(failed)
}

func (rt *Runtime) writeFirewall(st model.State) error {
	if rt.NFTPath == "" {
		return nil
	}
	policy := firewallPolicy(st)
	if rt.FallbackNFTPath != "" {
		// Persistent boot policy is always fail-closed for proxy clients. The
		// volatile policy overrides it in the same fw4 reload transaction;
		// after reboot that file is absent until the daemon has started.
		fallback := st
		fallback.Devices = append([]model.Device(nil), st.Devices...)
		for i := range fallback.Devices {
			if fallback.Devices[i].Mode == "proxy" {
				fallback.Devices[i].Mode = "blocked"
			}
		}
		if err := writePolicyFile(rt.FallbackNFTPath, firewallPolicy(fallback)); err != nil {
			return err
		}
	}
	if err := writePolicyFile(rt.NFTPath, policy); err != nil {
		return err
	}
	if nft, err := exec.LookPath("nft"); err == nil {
		cmd := exec.Command(nft, "-f", rt.NFTPath)
		if output, runErr := cmd.CombinedOutput(); runErr != nil {
			return fmt.Errorf("apply nftables policy: %s: %w", strings.TrimSpace(string(output)), runErr)
		}
	}
	return nil
}

func writePolicyFile(path, policy string) error {
	if current, err := os.ReadFile(path); err == nil && string(current) == policy {
		return nil // Avoid unnecessary writes to router flash.
	}
	if err := os.MkdirAll(filepath.Dir(path), 0755); err != nil {
		return err
	}
	if err := os.WriteFile(path+".tmp", []byte(policy), 0600); err != nil {
		return err
	}
	// fw4 may read this file concurrently; never expose a partial policy.
	return os.Rename(path+".tmp", path)
}

func firewallPolicy(st model.State) string {
	var proxy, direct, blocked []string
	for _, d := range st.Devices {
		ip := net.ParseIP(strings.TrimSpace(d.IP))
		if ip == nil || ip.To4() == nil {
			continue
		}
		switch d.Mode {
		case "proxy":
			proxy = append(proxy, ip.String())
		case "direct":
			direct = append(direct, ip.String())
		case "blocked", "":
			blocked = append(blocked, ip.String())
		}
	}
	var b strings.Builder
	b.WriteString("flush chain inet fw4 openpass_prerouting\n")
	b.WriteString("flush chain inet fw4 openpass_output\n")
	b.WriteString("flush chain inet fw4 openpass_forward\n")
	b.WriteString("flush chain inet fw4 openpass_dns_nat\n")
	b.WriteString("flush chain inet fw4 openpass_dns_output\n")
	b.WriteString("flush set inet fw4 proxy_clients\n")
	b.WriteString("flush set inet fw4 direct_clients\n")
	b.WriteString("flush set inet fw4 blocked_clients\n")
	if len(proxy) > 0 {
		b.WriteString("add element inet fw4 proxy_clients { ")
		b.WriteString(strings.Join(proxy, ", "))
		b.WriteString(" }\n")
	}
	if len(direct) > 0 {
		b.WriteString("add element inet fw4 direct_clients { ")
		b.WriteString(strings.Join(direct, ", "))
		b.WriteString(" }\n")
	}
	if len(blocked) > 0 {
		b.WriteString("add element inet fw4 blocked_clients { ")
		b.WriteString(strings.Join(blocked, ", "))
		b.WriteString(" }\n")
	}
	if st.Settings.Enabled {
		// Keep SSH, LuCI and self-service reachable even when a proxy fails.
		// The normal fw4 input chain still controls access to router services;
		// the separate DNS NAT chain continues to intercept port 53.
		// Do not let the local-destination exception bypass the IPv6 DNS
		// guard.  Link-local IPv6 DNS has no IPv4 device identity and must
		// fail closed rather than fall through to the router/WAN resolver.
		b.WriteString("add rule inet fw4 openpass_prerouting iifname \"br-lan\" meta nfproto ipv6 udp dport 53 drop\n")
		b.WriteString("add rule inet fw4 openpass_prerouting iifname \"br-lan\" meta nfproto ipv6 tcp dport 53 drop\n")
		b.WriteString("add rule inet fw4 openpass_prerouting fib daddr type local accept\n")
		b.WriteString("add rule inet fw4 openpass_prerouting ip saddr @blocked_clients drop\n")
		// A proxy client must never escape over normal WAN forwarding, even
		// in the interval between core exit and the process monitor running.
		b.WriteString("add rule inet fw4 openpass_forward ip saddr @blocked_clients drop\n")
		b.WriteString("add rule inet fw4 openpass_forward ip daddr { 10.0.0.0/8, 172.16.0.0/12, 192.168.0.0/16, 169.254.0.0/16 } accept\n")
		b.WriteString("add rule inet fw4 openpass_forward ip saddr @proxy_clients oifname != \"openpass0\" drop\n")
		b.WriteString("add rule inet fw4 openpass_dns_nat ip protocol udp udp dport 53 redirect to :1053\n")
		b.WriteString("add rule inet fw4 openpass_dns_nat ip protocol tcp tcp dport 53 redirect to :1053\n")
		// Router applications still use dnsmasq/loopback port 53. Redirect
		// their IPv4 and IPv6 queries to DoH before the output leak guard.
		// sing-box's upstream endpoints are literal IPs on HTTPS port 443,
		// so these rules cannot recursively capture its own DNS transport.
		b.WriteString("add rule inet fw4 openpass_dns_output meta nfproto ipv4 udp dport 53 redirect to :1053\n")
		b.WriteString("add rule inet fw4 openpass_dns_output meta nfproto ipv4 tcp dport 53 redirect to :1053\n")
		b.WriteString("add rule inet fw4 openpass_dns_output meta nfproto ipv6 udp dport 53 redirect to :1054\n")
		b.WriteString("add rule inet fw4 openpass_dns_output meta nfproto ipv6 tcp dport 53 redirect to :1054\n")
		b.WriteString("add rule inet fw4 openpass_prerouting ip protocol udp udp dport 853 drop\n")
		b.WriteString("add rule inet fw4 openpass_prerouting ip protocol tcp tcp dport 853 drop\n")
		b.WriteString("add rule inet fw4 openpass_prerouting ip saddr @proxy_clients accept\n")
		b.WriteString("add rule inet fw4 openpass_prerouting ip saddr @direct_clients accept\n")
		if st.Settings.DefaultMode != "direct" {
			b.WriteString("add rule inet fw4 openpass_prerouting iifname \"br-lan\" ip saddr != 0.0.0.0 drop\n")
		}
		b.WriteString("add rule inet fw4 openpass_prerouting iifname \"br-lan\" meta nfproto ipv6 drop\n")
		b.WriteString("add rule inet fw4 openpass_output udp dport 53 drop\n")
		b.WriteString("add rule inet fw4 openpass_output tcp dport 53 drop\n")
	}
	return b.String()
}

// ConfigJSON is useful to health endpoints and diagnostics without exposing
// secrets through the process command line.
func (rt *Runtime) ConfigJSON(st model.State) ([]byte, error) {
	cfg, err := singbox.Build(st)
	if err != nil {
		return nil, err
	}
	var compact any
	if err := json.Unmarshal(cfg, &compact); err != nil {
		return nil, err
	}
	return json.MarshalIndent(compact, "", "  ")
}
