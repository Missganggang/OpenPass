package api

import (
	"bufio"
	"context"
	"crypto/rand"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"
	"unicode/utf8"

	"openpass/internal/model"
	"openpass/internal/nodes"
	"openpass/internal/singbox"
	"openpass/internal/store"
)

type Server struct {
	Store               *store.Store
	ConfigPath          string
	SingBoxPath         string
	Version             string
	Runtime             interface{ Apply(model.State) error }
	discoverMu          sync.Mutex
	discoveryNeedsApply bool // protected by discoverMu
}

func New(s *store.Store) *Server {
	return &Server{Store: s, ConfigPath: "/tmp/openpass-sing-box.json", Version: "0.1.9"}
}
func (s *Server) Handler() http.Handler { return http.HandlerFunc(s.serve) }

func (s *Server) serve(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	if r.Method == http.MethodOptions {
		w.WriteHeader(http.StatusNoContent)
		return
	}
	p := strings.Trim(r.URL.Path, "/")
	if !strings.HasPrefix(p, "api/") {
		http.NotFound(w, r)
		return
	}
	p = strings.TrimPrefix(p, "api/")
	parts := strings.Split(strings.Trim(p, "/"), "/")
	if parts[0] == "" {
		writeJSON(w, map[string]any{"name": "OpenPass", "version": s.Version})
		return
	}
	var err error
	switch parts[0] {
	case "status":
		s.status(w, r)
	case "login":
		// OpenPass is intended for a trusted LAN. Keep the endpoint for the
		// bundled UI's optional login screen while leaving LAN-only installs
		// usable without an external identity service.
		if r.Method != http.MethodPost {
			err = methodErr()
			break
		}
		var body struct {
			Setup bool `json:"setup"`
		}
		_ = decode(r, &body)
		if body.Setup {
			writeJSON(w, map[string]any{"ok": true, "password": newPassword()})
		} else {
			writeJSON(w, map[string]any{"ok": true})
		}
	case "devices":
		err = s.devices(w, r, parts[1:])
	case "nodes":
		err = s.nodes(w, r, parts[1:])
	case "settings":
		err = s.settings(w, r)
	case "dns":
		if r.Method != http.MethodGet {
			err = methodErr()
		} else {
			writeJSON(w, s.Store.DNS())
		}
	case "self":
		s.self(w, r)
	case "apply":
		err = s.apply(w, r)
	case "config":
		if r.Method != http.MethodGet {
			err = methodErr()
		} else {
			s.config(w, r)
		}
	default:
		http.NotFound(w, r)
		return
	}
	if err != nil {
		statusErr(w, err)
	}
}
func methodErr() error { return &httpError{http.StatusMethodNotAllowed, "method not allowed"} }

type httpError struct {
	code int
	msg  string
}

func (e *httpError) Error() string { return e.msg }
func statusErr(w http.ResponseWriter, e error) {
	code := http.StatusBadRequest
	var he *httpError
	if errors.As(e, &he) {
		code = he.code
	} else if errors.Is(e, os.ErrNotExist) {
		code = http.StatusNotFound
	}
	http.Error(w, e.Error(), code)
}
func normalizeMode(v string) string {
	if strings.EqualFold(strings.TrimSpace(v), "block") {
		return "blocked"
	}
	if v == "" {
		return "blocked"
	}
	return v
}
func writeJSON(w http.ResponseWriter, v any) { _ = json.NewEncoder(w).Encode(v) }
func decode(r *http.Request, v any) error {
	b, e := io.ReadAll(io.LimitReader(r.Body, 2<<20))
	if e != nil {
		return e
	}
	if len(strings.TrimSpace(string(b))) == 0 {
		return nil
	}
	return json.Unmarshal(b, v)
}

// devicePolicyRequest tracks presence separately from value. This matters for
// PATCH: editing a label or visibility must not reset a device's DNS or
// binding, while an explicit null node_id is a request to clear a binding.
type devicePolicyRequest struct {
	mode, nodeID, dns        string
	modeSet, nodeSet, dnsSet bool
}

func parseDevicePolicyBody(body map[string]json.RawMessage) (devicePolicyRequest, error) {
	var req devicePolicyRequest
	if raw, ok := body["mode"]; ok {
		req.modeSet = true
		if string(raw) != "null" {
			if err := json.Unmarshal(raw, &req.mode); err != nil {
				return req, err
			}
		}
	}
	if raw, ok := body["node_id"]; ok {
		req.nodeSet = true
		if string(raw) != "null" {
			if err := json.Unmarshal(raw, &req.nodeID); err != nil {
				return req, err
			}
		}
	}
	if raw, ok := body["dns"]; ok {
		req.dnsSet = true
		if string(raw) != "null" {
			if err := json.Unmarshal(raw, &req.dns); err != nil {
				return req, err
			}
		}
	}
	return req, nil
}

func (s *Server) validateNode(id string) error {
	if strings.TrimSpace(id) == "" {
		return &httpError{http.StatusBadRequest, "代理模式必须选择一个节点"}
	}
	for _, n := range s.Store.Nodes() {
		if n.ID == id {
			if !n.Enabled {
				return &httpError{http.StatusBadRequest, "所选节点已停用"}
			}
			return nil
		}
	}
	return &httpError{http.StatusBadRequest, "所选节点不存在"}
}

func (s *Server) validateDNS(id string) error {
	if id == "" {
		return nil
	}
	if id == "custom" && strings.TrimSpace(s.Store.Settings().CustomDNS) != "" {
		return nil
	}
	for _, d := range s.Store.DNS() {
		if d.ID == id {
			return nil
		}
	}
	return &httpError{http.StatusBadRequest, "请选择有效的 DNS"}
}

// updateDevicePolicy applies a binding mutation and immediately reloads the
// runtime. Runtime reload is intentionally independent of AutoApply: a user
// explicitly choosing a device policy expects it to take effect at once.
func (s *Server) updateDevicePolicy(id string, req devicePolicyRequest, edit func(*model.Device) error) (model.Device, error) {
	var current model.Device
	found := false
	for _, d := range s.Store.Devices() {
		if d.ID == id {
			current, found = d, true
			break
		}
	}
	if !found {
		return model.Device{}, os.ErrNotExist
	}
	if edit != nil {
		if err := edit(&current); err != nil {
			return model.Device{}, err
		}
	}
	policyChanged := req.modeSet || req.nodeSet || req.dnsSet
	if !policyChanged {
		return s.Store.UpsertDevice(current)
	}
	return s.saveDevicePolicy(current, req)
}

func (s *Server) saveDevicePolicy(current model.Device, req devicePolicyRequest) (model.Device, error) {
	mode, nodeID, dns := current.Mode, current.NodeID, current.DNS
	if req.modeSet {
		mode = normalizeMode(strings.TrimSpace(req.mode))
		if mode != "proxy" && mode != "direct" && mode != "blocked" {
			return model.Device{}, &httpError{http.StatusBadRequest, "mode 必须是 proxy、direct 或 blocked"}
		}
	}
	if req.nodeSet {
		nodeID = strings.TrimSpace(req.nodeID)
		// Selecting a node is itself an explicit request for proxy mode. A
		// null/empty node with mode=proxy is rejected below instead of silently
		// producing a non-working policy.
		if nodeID != "" && !req.modeSet {
			mode = "proxy"
		}
	}
	if mode == "proxy" {
		if err := s.validateNode(nodeID); err != nil {
			return model.Device{}, err
		}
	} else {
		nodeID = ""
	}
	if req.dnsSet {
		dns = strings.TrimSpace(req.dns)
		if err := s.validateDNS(dns); err != nil {
			return model.Device{}, err
		}
	} else if req.modeSet || req.nodeSet {
		if mode == "direct" || mode == "blocked" {
			dns = "aliyun"
		} else if current.Mode != "proxy" {
			dns = "cloudflare"
		}
	}
	current.Mode, current.NodeID, current.DNS = mode, nodeID, dns
	updated, err := s.Store.UpsertDevicePolicy(current, mode == "proxy" && (req.modeSet || req.nodeSet))
	if err != nil {
		return model.Device{}, err
	}
	if s.Runtime != nil {
		if err := s.Runtime.Apply(s.Store.State()); err != nil {
			return model.Device{}, &httpError{http.StatusInternalServerError, fmt.Sprintf("选择已保存，但应用失败：%v", err)}
		}
	}
	return updated, nil
}

func updateDeviceMetadata(body map[string]json.RawMessage, d *model.Device) error {
	for key, target := range map[string]any{"hidden": &d.Hidden, "online": &d.Online, "remark": &d.Remark} {
		if raw, ok := body[key]; ok {
			if err := json.Unmarshal(raw, target); err != nil {
				return err
			}
		}
	}
	d.Remark = strings.TrimSpace(d.Remark)
	if utf8.RuneCountInString(d.Remark) > 200 {
		return &httpError{http.StatusBadRequest, "设备备注最多 200 个字符"}
	}
	return nil
}

func (s *Server) status(w http.ResponseWriter, _ *http.Request) {
	s.discoverDevices()
	online := 0
	for _, d := range s.Store.Devices() {
		if d.Online {
			online++
		}
	}
	kernelRunning := false
	if rt, ok := s.Runtime.(interface{ Running() bool }); ok {
		kernelRunning = rt.Running()
	}
	kernel := "sing-box 已停止"
	if kernelRunning {
		kernel = "sing-box 运行中"
	}
	writeJSON(w, map[string]any{"running": true, "kernel_running": kernelRunning, "kernel": kernel, "enabled": s.Store.Settings().Enabled, "version": s.Version, "online_devices": online, "devices": len(s.Store.Devices()), "nodes": len(s.Store.Nodes()), "kill_switch": s.Store.Settings().KillSwitch})
}

func (s *Server) devices(w http.ResponseWriter, r *http.Request, parts []string) error {
	if len(parts) == 0 {
		if r.Method == http.MethodGet {
			s.discoverDevices()
			hidden := r.URL.Query().Get("hidden")
			ds := s.Store.Devices()
			out := make([]model.Device, 0, len(ds))
			for _, d := range ds {
				if hidden == "true" && !d.Hidden {
					continue
				}
				if hidden != "true" && hidden != "all" && d.Hidden {
					continue
				}
				out = append(out, d)
			}
			writeJSON(w, out)
			return nil
		}
		if r.Method == http.MethodPost {
			s.discoverMu.Lock()
			defer s.discoverMu.Unlock()
			var d model.Device
			if e := decode(r, &d); e != nil {
				return e
			}
			if e := updateDeviceMetadata(nil, &d); e != nil {
				return e
			}
			mode := d.Mode
			if mode == "" {
				if d.NodeID != "" {
					mode = "proxy"
				} else {
					mode = s.Store.Settings().DefaultMode
				}
			}
			d.Mode = ""
			d, e := s.saveDevicePolicy(d, devicePolicyRequest{mode: mode, modeSet: true, nodeID: d.NodeID, nodeSet: true, dns: d.DNS, dnsSet: d.DNS != ""})
			if e == nil {
				writeJSON(w, d)
			}
			return e
		}
		return methodErr()
	}
	id := parts[0]
	if id == "hidden" && r.Method == http.MethodGet {
		ds := s.Store.Devices()
		out := make([]model.Device, 0)
		for _, d := range ds {
			if d.Hidden {
				out = append(out, d)
			}
		}
		writeJSON(w, out)
		return nil
	}
	if len(parts) >= 2 && parts[1] == "bind" {
		if r.Method != http.MethodPost {
			return methodErr()
		}
		s.discoverMu.Lock()
		defer s.discoverMu.Unlock()
		var body map[string]json.RawMessage
		if e := decode(r, &body); e != nil {
			return e
		}
		req, e := parseDevicePolicyBody(body)
		if e != nil {
			return e
		}
		d, e := s.updateDevicePolicy(id, req, nil)
		if e == nil {
			writeJSON(w, d)
		}
		return e
	}
	if r.Method == http.MethodPatch || r.Method == http.MethodPut {
		s.discoverMu.Lock()
		defer s.discoverMu.Unlock()
		var body map[string]json.RawMessage
		if e := decode(r, &body); e != nil {
			return e
		}
		req, e := parseDevicePolicyBody(body)
		if e != nil {
			return e
		}
		d, e := s.updateDevicePolicy(id, req, func(d *model.Device) error { return updateDeviceMetadata(body, d) })
		if e == nil {
			writeJSON(w, d)
		}
		return e
	}
	if r.Method == http.MethodDelete {
		s.discoverMu.Lock()
		defer s.discoverMu.Unlock()
		if err := s.Store.DeleteDevice(id); err != nil {
			return err
		}
		if s.Runtime != nil {
			if err := s.Runtime.Apply(s.Store.State()); err != nil {
				return &httpError{http.StatusInternalServerError, fmt.Sprintf("设备记录已释放，但策略重载失败：%v", err)}
			}
		}
		writeJSON(w, map[string]any{"released": true, "id": id})
		return nil
	}
	return methodErr()
}

func (s *Server) nodes(w http.ResponseWriter, r *http.Request, parts []string) error {
	if len(parts) == 0 {
		if r.Method == http.MethodGet {
			writeJSON(w, s.Store.Nodes())
			return nil
		}
		if r.Method == http.MethodPost {
			var n model.Node
			if e := decode(r, &n); e != nil {
				return e
			}
			if n.URI != "" {
				parsed, e := nodes.Parse(n.URI)
				if e != nil {
					return e
				}
				if n.Name != "" {
					parsed.Name = n.Name
				}
				n = parsed
			}
			if n.Type == "" {
				return fmt.Errorf("node type is required")
			}
			n.Enabled = true
			if x, e := s.Store.UpsertNode(n); e == nil {
				writeJSON(w, x)
			} else {
				return e
			}
			return nil
		}
		return methodErr()
	}
	if parts[0] == "export" {
		if r.Method != http.MethodGet {
			return methodErr()
		}
		return s.exportNodes(w, r)
	}
	if parts[0] == "import" {
		if r.Method != http.MethodPost {
			return methodErr()
		}
		return s.importNodes(w, r)
	}
	id := parts[0]
	if len(parts) > 1 && parts[1] == "export" {
		if r.Method != http.MethodGet {
			return methodErr()
		}
		return s.exportNode(w, r, id)
	}
	if len(parts) > 1 && parts[1] == "test" {
		if r.Method != http.MethodPost {
			return methodErr()
		}
		return s.testNode(w, r, id)
	}
	if r.Method == http.MethodDelete {
		// Serialize with device binding so a validated node cannot disappear
		// before the device policy is saved and applied.
		s.discoverMu.Lock()
		defer s.discoverMu.Unlock()
		if err := s.Store.DeleteNode(id); err != nil {
			var inUse *store.NodeInUseError
			if errors.As(err, &inUse) {
				w.WriteHeader(http.StatusConflict)
				writeJSON(w, map[string]any{"error": inUse.Error(), "bound_devices": inUse.Devices})
				return nil
			}
			return err
		}
		if s.Runtime != nil {
			if err := s.Runtime.Apply(s.Store.State()); err != nil {
				return &httpError{http.StatusInternalServerError, fmt.Sprintf("节点已删除，但配置重载失败：%v", err)}
			}
		}
		writeJSON(w, map[string]any{"deleted": true, "id": id})
		return nil
	}
	if r.Method == http.MethodPatch {
		var body map[string]json.RawMessage
		if e := decode(r, &body); e != nil {
			return e
		}
		x, e := s.Store.UpdateNode(id, func(n *model.Node) error {
			if raw, ok := body["name"]; ok {
				if err := json.Unmarshal(raw, &n.Name); err != nil {
					return err
				}
			}
			if raw, ok := body["remark"]; ok {
				if err := json.Unmarshal(raw, &n.Remark); err != nil {
					return err
				}
			}
			if raw, ok := body["enabled"]; ok {
				if err := json.Unmarshal(raw, &n.Enabled); err != nil {
					return err
				}
			}
			return nil
		})
		if e == nil {
			writeJSON(w, x)
		}
		return e
	}
	if r.Method == http.MethodPut {
		var n model.Node
		if e := decode(r, &n); e != nil {
			return e
		}
		n.ID = id
		x, e := s.Store.UpsertNode(n)
		if e == nil {
			writeJSON(w, x)
		}
		return e
	}
	return methodErr()
}

// exportNode writes a shareable link for one node.  Keeping this as a
// dedicated endpoint lets the UI copy a single credential without having to
// download the complete node list first.
func (s *Server) exportNode(w http.ResponseWriter, r *http.Request, id string) error {
	var node model.Node
	found := false
	for _, candidate := range s.Store.Nodes() {
		if candidate.ID == id {
			node = candidate
			found = true
			break
		}
	}
	if !found {
		return &httpError{http.StatusNotFound, "node not found"}
	}
	format := strings.ToLower(strings.TrimSpace(r.URL.Query().Get("format")))
	if format == "" {
		format = "uri"
	}
	switch format {
	case "uri", "text", "txt":
		uri, err := nodes.URI(node)
		if err != nil {
			return fmt.Errorf("export node %q: %w", node.ID, err)
		}
		w.Header().Set("Content-Type", "text/plain; charset=utf-8")
		w.Header().Set("Content-Disposition", `attachment; filename="openpass-node.txt"`)
		_, err = io.WriteString(w, uri)
		return err
	case "json":
		w.Header().Set("Content-Type", "application/json; charset=utf-8")
		w.Header().Set("Content-Disposition", `attachment; filename="openpass-node.json"`)
		return json.NewEncoder(w).Encode(node)
	default:
		return &httpError{http.StatusBadRequest, "format must be uri or json"}
	}
}

func (s *Server) exportNodes(w http.ResponseWriter, r *http.Request) error {
	ns := s.Store.Nodes()
	format := strings.ToLower(strings.TrimSpace(r.URL.Query().Get("format")))
	if format == "" {
		format = "uri"
	}
	switch format {
	case "json":
		w.Header().Set("Content-Type", "application/json; charset=utf-8")
		w.Header().Set("Content-Disposition", `attachment; filename="openpass-nodes.json"`)
		return json.NewEncoder(w).Encode(ns)
	case "uri", "text", "txt":
		lines := make([]string, 0, len(ns))
		for _, n := range ns {
			u, err := nodes.URI(n)
			if err != nil {
				return fmt.Errorf("export node %q: %w", n.ID, err)
			}
			lines = append(lines, u)
		}
		w.Header().Set("Content-Type", "text/plain; charset=utf-8")
		w.Header().Set("Content-Disposition", `attachment; filename="openpass-nodes.txt"`)
		_, err := io.WriteString(w, strings.Join(lines, "\n"))
		return err
	default:
		return &httpError{http.StatusBadRequest, "format must be uri or json"}
	}
}
func (s *Server) importNodes(w http.ResponseWriter, r *http.Request) error {
	var body struct {
		Content string `json:"content"`
		URI     string `json:"uri"`
		URL     string `json:"url"`
		Name    string `json:"name"`
	}
	if e := decode(r, &body); e != nil {
		return e
	}
	input := body.Content
	if input == "" {
		input = body.URI
	}
	if input == "" && body.URL != "" {
		ctx, cancel := context.WithTimeout(r.Context(), 10*time.Second)
		defer cancel()
		req, _ := http.NewRequestWithContext(ctx, http.MethodGet, body.URL, nil)
		resp, e := http.DefaultClient.Do(req)
		if e != nil {
			return e
		}
		defer resp.Body.Close()
		b, _ := io.ReadAll(io.LimitReader(resp.Body, 2<<20))
		input = string(b)
	}
	list, e := nodes.ParseMany(input)
	if e != nil {
		return e
	}
	out := make([]model.Node, 0, len(list))
	for _, n := range list {
		if body.Name != "" && len(list) == 1 {
			n.Name = body.Name
		}
		x, e := s.Store.UpsertNode(n)
		if e != nil {
			return e
		}
		out = append(out, x)
	}
	writeJSON(w, map[string]any{"imported": len(out), "nodes": out})
	return nil
}

func (s *Server) settings(w http.ResponseWriter, r *http.Request) error {
	if r.Method == http.MethodGet {
		writeJSON(w, s.Store.Settings())
		return nil
	}
	if r.Method != http.MethodPut && r.Method != http.MethodPatch {
		return methodErr()
	}
	s.discoverMu.Lock()
	defer s.discoverMu.Unlock()
	v := s.Store.Settings()
	if e := decode(r, &v); e != nil {
		return e
	}
	if v.DefaultMode == "" {
		v.DefaultMode = "direct"
	}
	v.DefaultMode = normalizeMode(v.DefaultMode)
	if v.DefaultMode == "block" {
		v.DefaultMode = "blocked"
	}
	if e := s.Store.UpdateSettings(v); e != nil {
		return e
	}
	if s.Runtime != nil && v.AutoApply {
		if e := s.Runtime.Apply(s.Store.State()); e != nil {
			return e
		}
	}
	writeJSON(w, v)
	return nil
}

func publicNodes(in []model.Node) []map[string]any {
	out := make([]map[string]any, 0, len(in))
	for _, n := range in {
		out = append(out, map[string]any{"id": n.ID, "name": n.Name, "remark": n.Remark, "type": n.Type, "address": n.Address, "port": n.Port, "enabled": n.Enabled})
	}
	return out
}
func selfPayload(d model.Device, ns []model.Node) map[string]any {
	return map[string]any{"device": d, "id": d.ID, "ip": d.IP, "mac": d.MAC, "hostname": d.Hostname, "remark": d.Remark, "online": d.Online, "hidden": d.Hidden, "mode": d.Mode, "node_id": d.NodeID, "dns": d.DNS, "nodes": publicNodes(ns)}
}
func (s *Server) apply(w http.ResponseWriter, r *http.Request) error {
	if r.Method != http.MethodPost {
		return methodErr()
	}
	s.discoverMu.Lock()
	defer s.discoverMu.Unlock()
	st := s.Store.State()
	if s.Runtime != nil {
		if e := s.Runtime.Apply(st); e != nil {
			return e
		}
		writeJSON(w, map[string]any{"applied": true, "config_path": s.ConfigPath})
		return nil
	}
	b, e := singbox.Build(st)
	if e != nil {
		return e
	}
	if s.ConfigPath != "" {
		if e = os.MkdirAll(filepath.Dir(s.ConfigPath), 0755); e != nil {
			return e
		}
		if e = os.WriteFile(s.ConfigPath, b, 0600); e != nil {
			return e
		}
	}
	writeJSON(w, map[string]any{"applied": true, "config_path": s.ConfigPath, "config": json.RawMessage(b)})
	return nil
}
func (s *Server) config(w http.ResponseWriter, _ *http.Request) {
	b, e := singbox.Build(s.Store.State())
	if e != nil {
		statusErr(w, e)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	w.Write(b)
}

// discoverDevices reads dnsmasq leases and the neighbour table. Both files
// are available on a stock OpenWrt install and do not require ubus plugins.
// Policies are keyed by MAC, so a DHCP address change never loses a binding.
func (s *Server) discoverDevices() {
	s.discoverMu.Lock()
	defer s.discoverMu.Unlock()
	type seenDevice struct {
		mac, ip, hostname string
		// online is deliberately based on a live neighbour entry. A DHCP
		// lease can remain in /tmp/dhcp.leases for hours after a client has
		// gone away, so the lease alone must never make a device online.
		online bool
	}
	seen := map[string]seenDevice{}
	now := time.Now()
	if f, err := os.Open("/tmp/dhcp.leases"); err == nil {
		defer f.Close()
		sc := bufio.NewScanner(f)
		for sc.Scan() {
			parts := strings.Fields(sc.Text())
			if len(parts) < 3 {
				continue
			}
			// dnsmasq writes: expiry mac ip hostname client-id. Ignore an
			// expired lease, but retain a zero expiry (static/infinite lease)
			// as an address hint until the neighbour table confirms activity.
			if expiry, ok := parseLeaseExpiry(parts[0]); ok && !leaseActive(expiry, now) {
				continue
			}
			mac := normalizeMAC(parts[1])
			ip := parts[2]
			if net.ParseIP(ip) == nil || mac == "" {
				continue
			}
			host := ""
			if len(parts) > 3 && parts[3] != "*" {
				host = parts[3]
			}
			seen[mac] = seenDevice{mac: mac, ip: ip, hostname: host}
		}
	}
	if ipCmd, err := exec.LookPath("ip"); err == nil {
		// br-lan is the client side of a normal OpenWrt bridge. Restricting
		// discovery to it keeps the WAN gateway and IPv6 link-local entries
		// out of the device list.
		out, _ := exec.Command(ipCmd, "neigh", "show", "dev", "br-lan").Output()
		if len(out) == 0 {
			out, _ = exec.Command(ipCmd, "neigh", "show").Output()
		}
		for _, line := range strings.Split(string(out), "\n") {
			parts := strings.Fields(line)
			// `ip neigh show dev br-lan` omits the `dev br-lan` tokens,
			// leaving four fields for a normal address/lladdr/state line.
			if len(parts) < 4 {
				continue
			}
			ip, mac := parts[0], ""
			for i := 1; i+1 < len(parts); i++ {
				if parts[i] == "lladdr" {
					mac = normalizeMAC(parts[i+1])
					break
				}
			}
			parsedIP := net.ParseIP(ip)
			if parsedIP == nil || parsedIP.To4() == nil || mac == "" {
				continue
			}
			state := neighborState(parts)
			if !neighborUsable(state) {
				continue
			}
			v := seen[mac]
			v.mac, v.ip, v.online = mac, ip, true
			// STALE means that the kernel has not heard from this address
			// recently. Probe it once so an idle but connected client can be
			// refreshed, while an abandoned stale entry becomes offline.
			if state == "STALE" {
				v.online = probeLANNeighbor(ip)
			}
			seen[mac] = v
		}
	}
	existing := s.Store.Devices()
	byMAC := map[string]model.Device{}
	for _, d := range existing {
		byMAC[normalizeMAC(d.MAC)] = d
	}
	for i, d := range existing {
		d.Online = false
		existing[i] = d
	}
	for _, v := range seen {
		d := byMAC[v.mac]
		if d.ID == "" {
			// An old lease is only an address hint. Recreate a released record
			// once the client is actually seen online, not on every refresh.
			if !v.online {
				continue
			}
			d.ID = store.ID("dev")
			d.Mode = s.Store.Settings().DefaultMode
			d.FirstSeen = time.Now()
		}
		if v.mac != "" {
			d.MAC = v.mac
		}
		if v.ip != "" {
			d.IP = v.ip
		}
		if v.hostname != "" {
			d.Hostname = v.hostname
		}
		if v.online {
			d.Online, d.LastSeen = true, now
		} else {
			d.Online = false
		}
		if _, err := s.Store.UpsertDevice(d); err != nil {
			continue
		}
	}
	for _, d := range existing {
		if _, ok := seen[normalizeMAC(d.MAC)]; !ok {
			_, _ = s.Store.UpsertDevice(d)
		}
	}
	if err := s.applyDiscoveredAddresses(existing); err != nil {
		log.Printf("apply discovered device addresses: %v", err)
	}
}

// A binding follows the MAC in storage, but sing-box and nftables match its
// current IPv4 address. Refresh those rules after DHCP moves a known device.
// Online timestamps and remarks alone must not restart all proxy connections.
// The caller holds discoverMu; a failed reload is retried on next discovery.
func (s *Server) applyDiscoveredAddresses(before []model.Device) error {
	addresses := make(map[string]string, len(before))
	for _, d := range before {
		addresses[d.ID] = d.IP
	}
	for _, d := range s.Store.Devices() {
		if previous, exists := addresses[d.ID]; exists && previous != d.IP {
			s.discoveryNeedsApply = true
		}
	}
	if !s.discoveryNeedsApply || s.Runtime == nil || !s.Store.Settings().Enabled {
		return nil
	}
	if err := s.Runtime.Apply(s.Store.State()); err != nil {
		return err
	}
	s.discoveryNeedsApply = false
	return nil
}

func normalizeMAC(v string) string {
	v = strings.ToUpper(strings.TrimSpace(v))
	if strings.Contains(v, "-") {
		v = strings.ReplaceAll(v, "-", ":")
	}
	return v
}

// parseLeaseExpiry parses the first field in a dnsmasq lease line. Older
// dnsmasq versions may write a non-numeric value while rotating the file; in
// that case the lease is retained as an address hint and still needs a live
// neighbour entry before it can be shown online.
func parseLeaseExpiry(v string) (time.Time, bool) {
	seconds, err := strconv.ParseInt(strings.TrimSpace(v), 10, 64)
	if err != nil || seconds < 0 {
		return time.Time{}, false
	}
	if seconds == 0 {
		return time.Time{}, true // static/infinite lease
	}
	return time.Unix(seconds, 0), true
}

func leaseActive(expiry time.Time, now time.Time) bool {
	return expiry.IsZero() || expiry.After(now)
}

// neighborState extracts the NUD state from `ip neigh` output. The state is
// normally the final token, but looking for the known values makes this work
// with both iproute2 and BusyBox output formats.
func neighborState(parts []string) string {
	for _, p := range parts {
		state := strings.ToUpper(strings.TrimSpace(p))
		switch state {
		case "REACHABLE", "STALE", "DELAY", "PROBE", "FAILED", "INCOMPLETE", "PERMANENT", "NOARP", "NONE":
			return state
		}
	}
	return ""
}

func neighborUsable(state string) bool {
	switch state {
	case "REACHABLE", "STALE", "DELAY", "PROBE":
		return true
	default:
		return false
	}
}

// probeLANNeighbor refreshes a stale neighbour entry. This avoids treating a
// client as offline merely because it has been idle long enough for the
// kernel to move its ARP entry to STALE. A failed probe is intentionally
// considered offline, which clears stale clients from the online list.
func probeLANNeighbor(ip string) bool {
	if net.ParseIP(ip) == nil {
		return false
	}
	ping, err := exec.LookPath("ping")
	if err != nil {
		return false
	}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	return exec.CommandContext(ctx, ping, "-c", "1", "-W", "1", ip).Run() == nil
}

func newPassword() string {
	const alphabet = "ABCDEFGHJKLMNPQRSTUVWXYZabcdefghijkmnopqrstuvwxyz23456789"
	b := make([]byte, 18)
	if _, err := rand.Read(b); err != nil {
		return "openpass-local"
	}
	for i := range b {
		b[i] = alphabet[int(b[i])%len(alphabet)]
	}
	return string(b)
}
