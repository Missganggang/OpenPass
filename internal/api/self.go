package api

import (
	"encoding/json"
	"fmt"
	"net"
	"net/http"
	"strings"

	"openpass/internal/model"
)

// Only the socket peer identifies a device. Forwarded headers and request
// bodies cannot select another client's binding on the LAN service.
func peerIP(remote string) string {
	host, _, err := net.SplitHostPort(remote)
	if err != nil {
		host = remote
	}
	host = strings.Split(strings.Trim(host, "[]"), "%")[0]
	if ip := net.ParseIP(host); ip != nil {
		return ip.String()
	}
	return ""
}

func (s *Server) self(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet && r.Method != http.MethodPost && r.Method != http.MethodPatch {
		statusErr(w, methodErr())
		return
	}
	settings := s.Store.Settings()
	if !settings.SelfServiceEnabled {
		statusErr(w, &httpError{http.StatusForbidden, "管理员已关闭设备自助服务"})
		return
	}
	s.discoverDevices()
	ip := peerIP(r.RemoteAddr)
	d := model.Device{IP: ip, Online: true, Mode: settings.DefaultMode}
	for _, candidate := range s.Store.Devices() {
		if ip != "" && peerIP(candidate.IP) == ip {
			d = candidate
			break
		}
	}
	identified := d.ID != "" && d.MAC != ""
	if r.Method != http.MethodGet {
		if !identified {
			statusErr(w, &httpError{http.StatusConflict, "尚未识别当前设备的 MAC，请连接本路由器局域网并刷新页面"})
			return
		}
		var body map[string]json.RawMessage
		if err := decode(r, &body); err != nil {
			statusErr(w, err)
			return
		}
		req, err := parseDevicePolicyBody(body)
		if err != nil {
			statusErr(w, err)
			return
		}
		if req.mode != "direct" && req.mode != "proxy" {
			statusErr(w, fmt.Errorf("请选择直连或代理节点"))
			return
		}
		s.discoverMu.Lock()
		updated, err := s.updateDevicePolicy(d.ID, req, nil)
		s.discoverMu.Unlock()
		if err != nil {
			statusErr(w, err)
			return
		}
		d = updated
		settings = s.Store.Settings()
	}
	nodes := make([]model.Node, 0)
	for _, n := range s.Store.Nodes() {
		if n.Enabled {
			nodes = append(nodes, n)
		}
	}
	payload := selfPayload(d, nodes)
	payload["enabled"] = settings.Enabled
	kernelRunning := false
	if rt, ok := s.Runtime.(interface{ Running() bool }); ok {
		kernelRunning = rt.Running()
	}
	payload["kernel_running"] = kernelRunning
	payload["self_service_enabled"] = settings.SelfServiceEnabled
	payload["device_identified"] = identified
	payload["can_bind"] = identified
	if !identified {
		payload["message"] = "已获取访问 IP，但路由器邻居表尚未识别 MAC；请使用本路由器 Wi-Fi/LAN 并刷新。"
	}
	writeJSON(w, payload)
}
