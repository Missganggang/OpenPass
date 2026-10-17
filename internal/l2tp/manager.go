// Package l2tp manages only OpenPass-owned OpenWrt netifd L2TP interfaces.
// It never starts or stops the shared xl2tpd service directly.
package l2tp

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"sync"
	"time"

	"openpass/internal/model"
)

type Status struct {
	State     string `json:"state"`
	Connected bool   `json:"connected"`
	Interface string `json:"interface"`
	Device    string `json:"device,omitempty"`
	LocalIP   string `json:"local_ip,omitempty"`
	Uptime    int64  `json:"uptime,omitempty"`
	LastError string `json:"last_error,omitempty"`
	pending   bool
}

type Dependencies struct {
	Ready   bool     `json:"ready"`
	Missing []string `json:"missing"`
}

// Runner makes command execution injectable without introducing a shell.
type Runner interface {
	Run(context.Context, string, ...string) ([]byte, error)
	Available(string) bool
	Exists(string) bool
}

type systemRunner struct{}

func (systemRunner) Run(ctx context.Context, name string, args ...string) ([]byte, error) {
	return exec.CommandContext(ctx, name, args...).CombinedOutput()
}
func (systemRunner) Available(name string) bool { _, err := exec.LookPath(name); return err == nil }
func (systemRunner) Exists(path string) bool    { _, err := os.Stat(path); return err == nil }

type Manager struct {
	mu           sync.Mutex
	runner       Runner
	errors       map[string]string
	notices      map[string]string
	desired      map[string]model.Node
	attempted    map[string]time.Time
	pendingSince map[string]time.Time
	enabled      bool
}

func New() *Manager { return NewWithRunner(systemRunner{}) }
func NewWithRunner(r Runner) *Manager {
	return &Manager{runner: r, errors: map[string]string{}, notices: map[string]string{}, desired: map[string]model.Node{}, attempted: map[string]time.Time{}, pendingSince: map[string]time.Time{}}
}

// netifd's l2tp protocol names the PPP device "l2tp-" + interface. Keep the
// interface at ten characters so the resulting Linux device fits IFNAMSIZ.
func InterfaceName(nodeID string) string {
	h := sha256.Sum256([]byte(nodeID))
	return fmt.Sprintf("op%x", h[:4])
}
func DeviceName(nodeID string) string { return "l2tp-" + InterfaceName(nodeID) }

func (m *Manager) Dependencies() Dependencies {
	d := Dependencies{Ready: true, Missing: []string{}}
	for _, name := range []string{"uci", "ubus", "ifup", "ifdown", "xl2tpd", "xl2tpd-control", "pppd"} {
		if !m.runner.Available(name) {
			d.Missing = append(d.Missing, name)
		}
	}
	if !m.runner.Exists("/lib/netifd/proto/l2tp.sh") {
		d.Missing = append(d.Missing, "netifd l2tp protocol")
	}
	if len(d.Missing) == 0 {
		// Installing a protocol script does not make an already running
		// netifd process register it. A normal UCI reload only applies config;
		// silently treating proto=none as a connection attempt is misleading.
		data, err := m.command("ubus", "call", "network", "get_proto_handlers")
		var handlers map[string]json.RawMessage
		if err != nil || json.Unmarshal(data, &handlers) != nil {
			d.Missing = append(d.Missing, "无法读取 netifd 协议状态，请检查网络服务")
		} else if handler, found := handlers["l2tp"]; !found || string(handler) == "null" {
			d.Missing = append(d.Missing, "netifd 尚未加载 L2TP，请重启路由器或网络服务后重试")
		}
	}
	d.Ready = len(d.Missing) == 0
	return d
}

func (m *Manager) command(name string, args ...string) ([]byte, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 12*time.Second)
	defer cancel()
	return m.runner.Run(ctx, name, args...)
}
