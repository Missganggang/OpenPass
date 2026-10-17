package api

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"strings"
	"unicode"
	"unicode/utf8"

	"openpass/internal/l2tp"
	"openpass/internal/model"
	"openpass/internal/store"
)

type l2tpRequest struct {
	Name     *string `json:"name"`
	Address  *string `json:"address"`
	Username *string `json:"username"`
	Password *string `json:"password"`
	Remark   *string `json:"remark"`
	MTU      *int    `json:"mtu"`
	Enabled  *bool   `json:"enabled"`
}

type l2tpConnection struct {
	model.Node
	Status       l2tp.Status `json:"status"`
	BoundDevices int         `json:"bound_devices"`
}

func decodeL2TP(r *http.Request) (l2tpRequest, error) {
	var body l2tpRequest
	decoder := json.NewDecoder(io.LimitReader(r.Body, 16<<10))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&body); err != nil {
		return body, &httpError{http.StatusBadRequest, "L2TP 配置格式不正确，请检查输入字段"}
	}
	if err := decoder.Decode(new(any)); err != io.EOF {
		return body, &httpError{http.StatusBadRequest, "L2TP 请求只能包含一个配置对象"}
	}
	return body, nil
}

func applyL2TPFields(n *model.Node, body l2tpRequest) error {
	for target, value := range map[*string]*string{&n.Name: body.Name, &n.Address: body.Address, &n.Username: body.Username, &n.Remark: body.Remark} {
		if value != nil {
			*target = *value
		}
	}
	// Empty edit passwords preserve the saved credential. This also makes
	// round-tripping the redacted list response safe.
	if body.Password != nil && *body.Password != "" {
		n.Password = *body.Password
	}
	if body.MTU != nil {
		n.MTU = *body.MTU
	}
	n.Type, n.Port, n.URI, n.Subscription = "l2tp", 1701, "", ""
	n.Name, n.Address, n.Remark = strings.TrimSpace(n.Name), strings.TrimSpace(n.Address), strings.TrimSpace(n.Remark)
	if n.MTU == 0 {
		n.MTU = 1400
	}
	if n.Name == "" || utf8.RuneCountInString(n.Name) > 100 || hasControl(n.Name) {
		return &httpError{http.StatusBadRequest, "连接名称不能为空，最多 100 个字符"}
	}
	if utf8.RuneCountInString(n.Remark) > 200 || hasControl(n.Remark) {
		return &httpError{http.StatusBadRequest, "备注最多 200 个字符，不能包含换行或控制字符"}
	}
	if !validL2TPServer(n.Address) {
		return &httpError{http.StatusBadRequest, "服务器地址必须是 IPv4 地址或域名，不要填写协议、端口或路径"}
	}
	for _, credential := range []string{n.Username, n.Password} {
		// OpenWrt's stock l2tp.sh writes these values into double-quoted PPP
		// options. Reject syntax that could escape that quoted value.
		if strings.TrimSpace(credential) == "" || len(credential) > 256 || !utf8.ValidString(credential) || hasControl(credential) || strings.ContainsAny(credential, "\"\\") {
			return &httpError{http.StatusBadRequest, "用户名和密码不能为空，最多 256 字节，且不能包含双引号、反斜杠或控制字符"}
		}
	}
	if n.MTU < 1280 || n.MTU > 1460 {
		return &httpError{http.StatusBadRequest, "MTU 必须在 1280 到 1460 之间"}
	}
	return nil
}

func hasControl(s string) bool {
	return strings.IndexFunc(s, unicode.IsControl) >= 0
}

func validL2TPServer(server string) bool {
	if ip := net.ParseIP(server); ip != nil {
		return ip.To4() != nil && !strings.Contains(server, ":")
	}
	if len(server) == 0 || len(server) > 253 {
		return false
	}
	// A dotted decimal address that failed ParseIP must not become a domain.
	if strings.Trim(server, "0123456789.") == "" {
		return false
	}
	for _, label := range strings.Split(server, ".") {
		if len(label) == 0 || len(label) > 63 || label[0] == '-' || label[len(label)-1] == '-' {
			return false
		}
		for _, c := range label {
			if (c < 'a' || c > 'z') && (c < 'A' || c > 'Z') && (c < '0' || c > '9') && c != '-' {
				return false
			}
		}
	}
	return true
}

func (s *Server) l2tpDependencies() l2tp.Dependencies {
	if rt, ok := s.Runtime.(interface{ L2TPDependencies() l2tp.Dependencies }); ok {
		return rt.L2TPDependencies()
	}
	return l2tp.Dependencies{Missing: []string{"OpenWrt L2TP 运行环境"}}
}

func (s *Server) l2tpConnections(ns []model.Node) []l2tpConnection {
	filtered := make([]model.Node, 0)
	for _, n := range ns {
		if n.Type == "l2tp" {
			filtered = append(filtered, n)
		}
	}
	statuses := make(map[string]l2tp.Status)
	disabled := s.Store.Settings().L2TPDisabled
	if rt, ok := s.Runtime.(interface {
		L2TPStatuses([]model.Node) map[string]l2tp.Status
	}); ok && !disabled {
		statuses = rt.L2TPStatuses(filtered)
	}
	counts := make(map[string]int)
	for _, d := range s.Store.Devices() {
		if d.NodeID != "" {
			counts[d.NodeID]++
		}
	}
	out := make([]l2tpConnection, 0, len(filtered))
	for _, n := range filtered {
		status, found := statuses[n.ID]
		if disabled {
			status = l2tp.Status{State: "disabled"}
		} else if !found {
			status.State = "disconnected"
			if n.Enabled {
				status.State = "error"
				status.LastError = "无法读取 L2TP 接口状态，请检查 OpenWrt 运行环境"
			}
		}
		n.Password, n.URI = "", ""
		out = append(out, l2tpConnection{Node: n, Status: status, BoundDevices: counts[n.ID]})
	}
	return out
}

func (s *Server) findL2TP(id string) (model.Node, error) {
	for _, n := range s.Store.Nodes() {
		if n.ID == id && n.Type == "l2tp" {
			return n, nil
		}
	}
	return model.Node{}, &httpError{http.StatusNotFound, "L2TP 连接不存在"}
}

func (s *Server) l2tp(w http.ResponseWriter, r *http.Request, parts []string) error {
	if len(parts) == 0 && r.Method == http.MethodGet {
		enabled := !s.Store.Settings().L2TPDisabled
		var dependencies any
		if enabled {
			deps := s.l2tpDependencies()
			missing := deps.Missing
			if missing == nil {
				missing = []string{}
			}
			dependencies = map[string]any{"available": deps.Ready, "missing": missing}
		}
		writeJSON(w, map[string]any{"enabled": enabled, "apply_error": s.l2tpModuleError(), "connections": s.l2tpConnections(s.Store.Nodes()), "dependencies": dependencies})
		return nil
	}
	// Share the device-policy lock with binding and deletion, so no request
	// can disable/delete a connection in the middle of binding a client.
	s.discoverMu.Lock()
	defer s.discoverMu.Unlock()
	if len(parts) == 1 && parts[0] == "settings" {
		return s.l2tpSettings(w, r)
	}
	if len(parts) == 1 && parts[0] == "import" {
		return s.l2tpImport(w, r)
	}
	if len(parts) == 0 {
		if r.Method != http.MethodPost {
			return methodErr()
		}
		body, err := decodeL2TP(r)
		if err != nil {
			return err
		}
		if body.Enabled != nil && *body.Enabled {
			return &httpError{http.StatusBadRequest, "请先保存 L2TP 连接，再点击连接按钮"}
		}
		n := model.Node{Enabled: false}
		if err := applyL2TPFields(&n, body); err != nil {
			return err
		}
		n, err = s.Store.UpsertNode(n)
		if err != nil {
			return err
		}
		writeJSON(w, s.l2tpConnections([]model.Node{n})[0])
		return nil
	}
	if len(parts) > 2 {
		return &httpError{http.StatusNotFound, "L2TP 操作不存在"}
	}
	n, err := s.findL2TP(parts[0])
	if err != nil {
		return err
	}
	if len(parts) == 2 {
		return s.l2tpAction(w, r, n, parts[1])
	}
	switch r.Method {
	case http.MethodGet:
		writeJSON(w, s.l2tpConnections([]model.Node{n})[0])
		return nil
	case http.MethodPatch:
		body, err := decodeL2TP(r)
		if err != nil {
			return err
		}
		if body.Enabled != nil {
			return &httpError{http.StatusBadRequest, "请使用连接或断开按钮修改连接状态"}
		}
		if err := applyL2TPFields(&n, body); err != nil {
			return err
		}
		n, err = s.Store.UpsertNode(n)
		if err != nil {
			return err
		}
		if n.Enabled && !s.Store.Settings().L2TPDisabled {
			if err := s.applyL2TPState(); err != nil {
				return err
			}
		}
		writeJSON(w, s.l2tpConnections([]model.Node{n})[0])
		return nil
	case http.MethodDelete:
		if err := s.Store.DeleteNode(n.ID); err != nil {
			var inUse *store.NodeInUseError
			if errors.As(err, &inUse) {
				w.WriteHeader(http.StatusConflict)
				writeJSON(w, map[string]any{"error": inUse.Error(), "bound_devices": inUse.Devices})
				return nil
			}
			return err
		}
		if err := s.applyL2TPState(); err != nil {
			return err
		}
		writeJSON(w, map[string]any{"deleted": true, "id": n.ID})
		return nil
	default:
		return methodErr()
	}
}

func (s *Server) applyL2TPState() error {
	if s.Runtime != nil {
		if err := s.Runtime.Apply(s.Store.State()); err != nil {
			return &httpError{http.StatusInternalServerError, fmt.Sprintf("L2TP 配置已保存，但应用失败：%v", err)}
		}
	}
	return nil
}

func (s *Server) l2tpAction(w http.ResponseWriter, r *http.Request, n model.Node, action string) error {
	if r.Method != http.MethodPost {
		return methodErr()
	}
	if action == "credentials" {
		writeJSON(w, map[string]string{"address": n.Address, "username": n.Username, "password": n.Password})
		return nil
	}
	if action != "connect" && action != "disconnect" && action != "reconnect" {
		return &httpError{http.StatusNotFound, "L2TP 操作不存在"}
	}
	if action != "disconnect" {
		if s.Store.Settings().L2TPDisabled {
			return &httpError{http.StatusConflict, "L2TP 功能已关闭，请先打开 L2TP 功能"}
		}
		deps := s.l2tpDependencies()
		if s.Runtime == nil || !deps.Ready {
			message := "L2TP 依赖尚未就绪，请在路由器安装或更新所需组件后重试"
			if len(deps.Missing) > 0 {
				message += "：" + strings.Join(deps.Missing, "、")
			}
			return &httpError{http.StatusConflict, message}
		}
		if err := applyL2TPFields(&n, l2tpRequest{}); err != nil {
			return err
		}
		if action == "reconnect" {
			if _, ok := s.Runtime.(interface{ ReconnectL2TP(string) error }); !ok {
				return &httpError{http.StatusConflict, "当前运行环境不支持 L2TP 重连"}
			}
		}
	}
	n.Enabled = action != "disconnect"
	n, err := s.Store.UpsertNodePolicy(n, n.Enabled)
	if err != nil {
		return err
	}
	if err := s.applyL2TPState(); err != nil {
		return err
	}
	if action == "reconnect" {
		if err := s.Runtime.(interface{ ReconnectL2TP(string) error }).ReconnectL2TP(n.ID); err != nil {
			return &httpError{http.StatusInternalServerError, fmt.Sprintf("L2TP 重连失败：%v", err)}
		}
	}
	writeJSON(w, s.l2tpConnections([]model.Node{n})[0])
	return nil
}
