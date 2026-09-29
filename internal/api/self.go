package api

import (
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
		var body struct {
			Mode   string `json:"mode"`
			NodeID string `json:"node_id"`
			DNS    string `json:"dns"`
		}
		if err := decode(r, &body); err != nil {
			statusErr(w, err)
			return
		}
		if body.Mode != "direct" && body.Mode != "proxy" {
			statusErr(w, fmt.Errorf("请选择直连或代理节点"))
			return
		}
		if body.Mode == "proxy" {
			valid := false
			for _, n := range s.Store.Nodes() {
				if n.ID == body.NodeID && n.Enabled {
					valid = true
					break
				}
			}
			if !valid {
				statusErr(w, fmt.Errorf("请选择一个存在且已启用的节点"))
				return
			}
		} else {
			body.NodeID = ""
		}
		if body.DNS != "" {
			valid := false
			for _, dns := range s.Store.DNS() {
				if dns.ID == body.DNS {
					valid = true
					break
				}
			}
			if !valid {
				statusErr(w, fmt.Errorf("请选择有效的 DNS"))
				return
			}
		}
		updated, err := s.Store.UpdateDevice(d.ID, func(v *model.Device) {
			v.Mode, v.NodeID = body.Mode, body.NodeID
			if body.DNS != "" {
				v.DNS = body.DNS
			}
		})
		if err != nil {
			statusErr(w, err)
			return
		}
		if s.Runtime != nil && settings.AutoApply {
			if err := s.Runtime.Apply(s.Store.State()); err != nil {
				statusErr(w, fmt.Errorf("选择已保存，但应用失败：%w", err))
				return
			}
		}
		d = updated
	}
	nodes := make([]model.Node, 0)
	for _, n := range s.Store.Nodes() {
		if n.Enabled {
			nodes = append(nodes, n)
		}
	}
	payload := selfPayload(d, nodes)
	payload["enabled"] = settings.Enabled
	payload["self_service_enabled"] = settings.SelfServiceEnabled
	payload["device_identified"] = identified
	payload["can_bind"] = identified
	if !identified {
		payload["message"] = "已获取访问 IP，但路由器邻居表尚未识别 MAC；请使用本路由器 Wi-Fi/LAN 并刷新。"
	}
	writeJSON(w, payload)
}
