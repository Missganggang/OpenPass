package l2tp

import (
	"errors"
	"fmt"
	"sort"
	"strings"
	"time"

	"openpass/internal/model"
)

const connectionTimeout = 90 * time.Second

// Reconcile keeps UCI persistent configuration inert (auto=0). Only this
// running manager explicitly requests ifup, so disabling OpenPass cannot
// leave its L2TP connections automatically starting at the next boot.
func (m *Manager) Reconcile(nodes []model.Node, enabled bool) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.enabled = enabled
	m.desired = map[string]model.Node{}
	byInterface := map[string]model.Node{}
	wantsConnections := false
	var failures []error
	for _, n := range nodes {
		if n.Type != "l2tp" {
			continue
		}
		m.desired[n.ID] = n
		if err := Validate(n); err != nil {
			m.errors[n.ID] = err.Error()
			failures = append(failures, err)
			continue
		}
		name := InterfaceName(n.ID)
		if previous, exists := byInterface[name]; exists && previous.ID != n.ID {
			return fmt.Errorf("L2TP 接口标识冲突，请重新添加节点")
		}
		byInterface[name] = n
		wantsConnections = wantsConnections || (enabled && n.Enabled)
	}
	if !m.runner.Available("uci") {
		if !wantsConnections {
			return errors.Join(failures...)
		}
		return m.dependencyFailure()
	}
	sections, err := m.readConfig()
	if err != nil {
		return err
	}
	// Refuse a collision before issuing any mutation. A similar name alone
	// is never sufficient evidence that an interface belongs to OpenPass.
	for name, n := range byInterface {
		if existing, found := sections[name]; found && !isOwned(name, existing) {
			err := fmt.Errorf("L2TP 接口 %s 已由其他配置使用", name)
			m.errors[n.ID] = err.Error()
			return err
		}
	}
	deps := m.Dependencies()
	if wantsConnections && !deps.Ready {
		// Existing owned tunnels must still be stopped below when disabled or
		// removed, even if a package was uninstalled since their creation.
		failures = append(failures, m.dependencyFailure())
	}
	changes := map[string]map[string]string{}
	for name, existing := range sections {
		if !isOwned(name, existing) {
			continue
		}
		n, wanted := byInterface[name]
		changed := wanted && !sameConfig(existing, configFor(n))
		if !wanted || changed || !enabled || !n.Enabled {
			if err := m.down(name); err != nil {
				m.errors[existing["openpass_node"]] = err.Error()
				return err
			}
		}
		if !wanted {
			changes[name] = nil
		}
	}
	for name, n := range byInterface {
		// Saving a disabled L2TP node requires only state.json. In particular,
		// missing optional packages must not block ordinary proxy devices.
		if !deps.Ready {
			continue
		}
		wanted := configFor(n)
		if !sameConfig(sections[name], wanted) {
			changes[name] = wanted
		}
	}
	if len(changes) > 0 {
		if err := m.writeChanges(sections, changes); err != nil {
			return err
		}
	}
	for name, n := range byInterface {
		if !enabled || !n.Enabled {
			delete(m.attempted, n.ID)
			delete(m.pendingSince, n.ID)
			delete(m.notices, n.ID)
			if deps.Ready {
				delete(m.errors, n.ID)
			}
			continue
		}
		if !deps.Ready {
			continue
		}
		_, configChanged := changes[name]
		current := m.readStatus(n.ID)
		if current.Connected {
			delete(m.errors, n.ID)
			delete(m.pendingSince, n.ID)
			delete(m.notices, n.ID)
			continue
		}
		if (current.State == "connecting" || current.pending) && !configChanged {
			started, tracked := m.pendingSince[n.ID]
			if !tracked {
				// After a daemon restart, give a pending netifd connection its
				// own full grace period; the previous process's age is unknown.
				m.pendingSince[n.ID] = time.Now()
				continue
			}
			if time.Since(started) < connectionTimeout {
				continue
			}
			// netifd can stay pending indefinitely after a broken L2TP tunnel.
			// Retire only this owned LAC/interface, without restarting xl2tpd
			// or touching another client connection. Reset the clock even when
			// down fails, so a failure cannot cause a tight reconnect loop.
			m.pendingSince[n.ID], m.attempted[n.ID] = time.Now(), time.Now()
			if err := m.down(name); err != nil {
				err = fmt.Errorf("L2TP 连接超时，断开重试失败：%w", err)
				m.errors[n.ID] = err.Error()
				failures = append(failures, err)
				continue
			}
			if err := m.up(n.ID); err != nil {
				failures = append(failures, err)
			} else {
				m.notices[n.ID] = "上次 L2TP 连接超过 90 秒未完成，已重新拨号"
			}
			continue
		}
		delete(m.pendingSince, n.ID)
		if !configChanged && time.Since(m.attempted[n.ID]) < 30*time.Second {
			continue
		}
		if err := m.up(n.ID); err != nil {
			failures = append(failures, err)
		}
	}
	for id := range m.attempted {
		if _, ok := m.desired[id]; !ok {
			delete(m.attempted, id)
			delete(m.errors, id)
			delete(m.pendingSince, id)
			delete(m.notices, id)
		}
	}
	return errors.Join(failures...)
}

func (m *Manager) dependencyFailure() error {
	d := m.Dependencies()
	err := fmt.Errorf("L2TP 依赖未安装：%s", strings.Join(d.Missing, ", "))
	for id, n := range m.desired {
		if m.enabled && n.Enabled {
			m.errors[id] = err.Error()
		}
	}
	return err
}

func (m *Manager) readConfig() (map[string]map[string]string, error) {
	data, err := m.command("uci", "-q", "show", "network")
	if err != nil {
		return nil, fmt.Errorf("无法读取 OpenWrt 网络配置")
	}
	return parseNetwork(data), nil
}

func (m *Manager) writeChanges(existing, changes map[string]map[string]string) error {
	names := make([]string, 0, len(changes))
	for name := range changes {
		names = append(names, name)
	}
	sort.Strings(names)
	var staged []string
	revert := func() {
		for _, name := range staged {
			_, _ = m.command("uci", "-q", "revert", "network."+name)
		}
	}
	for _, name := range names {
		staged = append(staged, name)
		if _, found := existing[name]; found {
			if _, err := m.command("uci", "-q", "delete", "network."+name); err != nil {
				revert()
				return fmt.Errorf("无法替换 OpenPass L2TP 接口配置")
			}
		}
		if changes[name] == nil {
			continue
		}
		for _, key := range sortedKeys(changes[name]) {
			path := "network." + name
			if key != "" {
				path += "." + key
			}
			if _, err := m.command("uci", "set", path+"="+changes[name][key]); err != nil {
				revert()
				return fmt.Errorf("无法写入 OpenPass L2TP 接口配置")
			}
		}
	}
	if _, err := m.command("uci", "commit", "network"); err != nil {
		revert()
		return fmt.Errorf("无法保存 OpenPass L2TP 接口配置")
	}
	// netifd reload is required only after UCI changed. It applies the delta;
	// unlike network restart it does not tear down unrelated interfaces.
	if _, err := m.command("ubus", "call", "network", "reload"); err != nil {
		return fmt.Errorf("netifd 未能加载新的 L2TP 接口配置")
	}
	return nil
}

func (m *Manager) up(nodeID string) error {
	m.attempted[nodeID] = time.Now()
	m.pendingSince[nodeID] = m.attempted[nodeID]
	delete(m.notices, nodeID)
	if _, err := m.command("ifup", InterfaceName(nodeID)); err != nil {
		err := fmt.Errorf("L2TP 连接请求失败，请检查 netifd 与 xl2tpd")
		m.errors[nodeID] = err.Error()
		return err
	}
	delete(m.errors, nodeID)
	return nil
}

func (m *Manager) down(name string) error {
	if _, err := m.command("ifdown", name); err != nil {
		// A committed but not yet loaded interface need not have a ubus object.
		// Only tolerate that case; never delete a configuration while its live
		// interface might still be using network policy or credentials.
		if m.runner.Exists("/sys/class/net/l2tp-" + name) {
			return fmt.Errorf("无法断开 L2TP 接口 %s", name)
		}
		data, statusErr := m.command("ubus", "call", "network.interface."+name, "status")
		if statusErr == nil {
			raw, parseErr := decodeStatus(data)
			if parseErr != nil || raw.Up || raw.Pending {
				return fmt.Errorf("无法确认 L2TP 接口 %s 已断开", name)
			}
		} else if !m.interfaceAbsent(name) {
			return fmt.Errorf("无法确认 L2TP 接口 %s 已断开", name)
		}
	}
	return m.waitDown(name)
}

func (m *Manager) interfaceAbsent(name string) bool {
	// A failed status query can mean either "object not found" or a dead
	// ubus connection. Only a successful empty inventory proves absence.
	data, err := m.command("ubus", "-S", "list", "network.interface."+name)
	return err == nil && strings.TrimSpace(string(data)) == ""
}

// Reconnect operates on a desired, enabled, positively owned interface only.
func (m *Manager) Reconnect(nodeID string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	n, found := m.desired[nodeID]
	if !found || !m.enabled || !n.Enabled {
		return fmt.Errorf("请先启用 OpenPass 与该 L2TP 节点")
	}
	if err := Validate(n); err != nil {
		return err
	}
	if !m.Dependencies().Ready {
		return m.dependencyFailure()
	}
	sections, err := m.readConfig()
	if err != nil {
		return err
	}
	name := InterfaceName(nodeID)
	if !isOwned(name, sections[name]) {
		return fmt.Errorf("找不到 OpenPass 管理的 L2TP 接口")
	}
	if err := m.down(name); err != nil {
		return err
	}
	return m.up(nodeID)
}

// Cleanup disconnects only owned interfaces. Configuration is retained on a
// normal stop and removed during uninstall. No shared daemon is terminated.
func (m *Manager) Cleanup(removeConfigs bool) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.enabled = false
	if !m.runner.Available("uci") {
		return nil
	}
	sections, err := m.readConfig()
	if err != nil {
		return err
	}
	changes := map[string]map[string]string{}
	var failures []error
	for name, c := range sections {
		if !isOwned(name, c) {
			continue
		}
		if err := m.down(name); err != nil {
			failures = append(failures, err)
			continue
		}
		if removeConfigs {
			changes[name] = nil
		}
	}
	if len(changes) > 0 {
		if err := m.writeChanges(sections, changes); err != nil {
			failures = append(failures, err)
		}
	}
	return errors.Join(failures...)
}
