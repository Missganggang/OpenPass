package l2tp

import (
	"encoding/json"
	"fmt"
	"net"
	"time"

	"openpass/internal/model"
)

type netifdStatus struct {
	Up        bool   `json:"up"`
	Pending   bool   `json:"pending"`
	Available bool   `json:"available"`
	L3Device  string `json:"l3_device"`
	Uptime    int64  `json:"uptime"`
	IPv4      []struct {
		Address string `json:"address"`
	} `json:"ipv4-address"`
	Errors []struct {
		Code string `json:"code"`
	} `json:"errors"`
}

func decodeStatus(data []byte) (netifdStatus, error) {
	var s netifdStatus
	err := json.Unmarshal(data, &s)
	return s, err
}

func (m *Manager) Status(nodeID string) Status {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.statusLocked(nodeID)
}

func (m *Manager) Statuses(nodes []model.Node) map[string]Status {
	m.mu.Lock()
	defer m.mu.Unlock()
	result := map[string]Status{}
	for _, n := range nodes {
		if n.Type == "l2tp" {
			result[n.ID] = m.statusLocked(n.ID)
		}
	}
	return result
}

func (m *Manager) statusLocked(nodeID string) Status {
	s := m.readStatus(nodeID)
	if s.Connected {
		delete(m.errors, nodeID)
		delete(m.pendingSince, nodeID)
		delete(m.notices, nodeID)
		return s
	}
	if e := m.errors[nodeID]; e != "" {
		s.State, s.LastError = "error", e
	} else if s.State == "connecting" || s.pending {
		if started, tracked := m.pendingSince[nodeID]; tracked && time.Since(started) >= connectionTimeout {
			s.State, s.LastError = "error", "L2TP 连接超过 90 秒未完成，等待自动重试"
		} else if notice := m.notices[nodeID]; notice != "" {
			s.LastError = notice
		}
	}
	return s
}

func (m *Manager) readStatus(nodeID string) Status {
	s := Status{State: "disconnected", Interface: InterfaceName(nodeID)}
	if !m.runner.Available("ubus") {
		s.State, s.LastError = "error", "当前系统未提供 OpenWrt netifd/ubus"
		return s
	}
	data, err := m.command("ubus", "call", "network.interface."+s.Interface, "status")
	if err != nil {
		if attempt, ok := m.attempted[nodeID]; ok && time.Since(attempt) < 15*time.Second {
			s.State = "connecting"
		}
		return s
	}
	raw, err := decodeStatus(data)
	if err != nil {
		s.State, s.LastError = "error", "无法读取 L2TP 接口状态"
		return s
	}
	s.pending = raw.Pending
	if raw.Up {
		if raw.L3Device != DeviceName(nodeID) {
			s.State, s.LastError = "error", "L2TP 接口设备名称异常，已禁止使用该接口"
			return s
		}
		if !m.runner.Exists("/sys/class/net/" + raw.L3Device) {
			s.State, s.LastError = "error", "L2TP 虚拟网卡尚未就绪"
			return s
		}
		for _, item := range raw.IPv4 {
			if ip := net.ParseIP(item.Address); ip != nil && ip.To4() != nil {
				s.LocalIP = ip.String()
				break
			}
		}
		if s.LocalIP == "" {
			s.State = "connecting"
			return s
		}
		s.State, s.Connected, s.Device, s.Uptime = "connected", true, raw.L3Device, raw.Uptime
		return s
	}
	if len(raw.Errors) > 0 {
		s.State, s.LastError = "error", "L2TP 拨号失败，请检查服务器、账号和网络"
		for _, e := range raw.Errors {
			switch e.Code {
			case "AUTH_FAILED", "AUTHENTICATION_FAILED":
				s.LastError = "L2TP 认证失败，请检查用户名和密码"
			case "PEER_AUTH_FAILED":
				s.LastError = "L2TP 对端认证失败"
			case "MISSING_PEER_ADDRESS", "INVALID_SERVER", "RESOLVE_FAILED":
				s.LastError = "无法解析 L2TP 服务器地址"
			case "CONNECT_FAILED", "CONNECTION_FAILED":
				s.LastError = "无法连接 L2TP 服务器"
			}
		}
		return s
	}
	if raw.Pending {
		s.State = "connecting"
	}
	return s
}

func (m *Manager) waitDown(name string) error {
	deadline := time.Now().Add(8 * time.Second)
	for {
		deviceExists := m.runner.Exists("/sys/class/net/l2tp-" + name)
		data, err := m.command("ubus", "call", "network.interface."+name, "status")
		if err != nil && !deviceExists && m.interfaceAbsent(name) {
			return nil
		}
		if err == nil {
			raw, parseErr := decodeStatus(data)
			if parseErr == nil && !raw.Up && !raw.Pending && !deviceExists {
				return nil
			}
		}
		if time.Now().After(deadline) {
			return fmt.Errorf("L2TP 接口 %s 尚未完成断开，请稍后重试", name)
		}
		time.Sleep(100 * time.Millisecond)
	}
}
