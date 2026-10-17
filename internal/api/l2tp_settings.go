package api

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"

	"openpass/internal/model"
)

func (s *Server) l2tpModuleError() string {
	// This runtime accessor only reads the result of its last Apply, including
	// startup restoration failures. It must not probe a disabled module.
	if rt, ok := s.Runtime.(interface{ L2TPModuleError() string }); ok {
		return rt.L2TPModuleError()
	}
	return ""
}

func (s *Server) l2tpSettingsPayload() map[string]any {
	return map[string]any{"enabled": !s.Store.Settings().L2TPDisabled, "apply_error": s.l2tpModuleError()}
}

func (s *Server) l2tpSettingsError(w http.ResponseWriter, err error) {
	payload := s.l2tpSettingsPayload()
	payload["error"] = err.Error()
	code := http.StatusBadRequest
	var he *httpError
	if errors.As(err, &he) {
		code = he.code
	}
	if code == http.StatusInternalServerError && payload["apply_error"] == "" {
		payload["apply_error"] = err.Error()
	}
	w.WriteHeader(code)
	writeJSON(w, payload)
}

func (s *Server) l2tpSettings(w http.ResponseWriter, r *http.Request) error {
	if r.Method == http.MethodGet {
		writeJSON(w, s.l2tpSettingsPayload())
		return nil
	}
	if r.Method != http.MethodPatch {
		return methodErr()
	}
	var body struct {
		Enabled *bool `json:"enabled"`
	}
	decoder := json.NewDecoder(io.LimitReader(r.Body, 1024))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&body); err != nil || body.Enabled == nil {
		return &httpError{http.StatusBadRequest, "请指定 L2TP 功能是否开启（enabled）"}
	}
	if err := decoder.Decode(new(any)); err != io.EOF {
		return &httpError{http.StatusBadRequest, "L2TP 设置请求格式不正确"}
	}
	if _, err := s.Store.SetL2TPEnabled(*body.Enabled); err != nil {
		s.l2tpSettingsError(w, err)
		return nil
	}
	// Explicit module changes apply regardless of the general auto-apply option.
	if err := s.applyL2TPState(); err != nil {
		s.l2tpSettingsError(w, err)
		return nil
	}
	writeJSON(w, s.l2tpSettingsPayload())
	return nil
}

const maxL2TPImportBytes = 1 << 20

func (s *Server) l2tpImport(w http.ResponseWriter, r *http.Request) error {
	if r.Method != http.MethodPost {
		return methodErr()
	}
	raw, err := io.ReadAll(io.LimitReader(r.Body, maxL2TPImportBytes+1))
	if err != nil || len(raw) > maxL2TPImportBytes {
		return &httpError{http.StatusBadRequest, "批量导入内容不能超过 1 MiB"}
	}
	var body struct {
		Text string `json:"text"`
	}
	decoder := json.NewDecoder(strings.NewReader(string(raw)))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&body); err != nil {
		return &httpError{http.StatusBadRequest, "批量导入格式不正确"}
	}
	if err := decoder.Decode(new(any)); err != io.EOF {
		return &httpError{http.StatusBadRequest, "批量导入请求只能包含一个对象"}
	}
	nodes := make([]model.Node, 0)
	for index, line := range strings.Split(body.Text, "\n") {
		// Remove only the Windows line ending, preserving meaningful credential
		// whitespace. Blank lines are ignored and errors retain source line numbers.
		line = strings.TrimSuffix(line, "\r")
		if strings.TrimSpace(line) == "" {
			continue
		}
		if len(nodes) == 500 {
			return &httpError{http.StatusBadRequest, "每次最多导入 500 条 L2TP 连接"}
		}
		fields := strings.SplitN(line, ":", 3)
		if len(fields) != 3 {
			return &httpError{http.StatusBadRequest, fmt.Sprintf("第 %d 行格式不正确，请使用服务器:用户名:密码", index+1)}
		}
		n := model.Node{Address: strings.TrimSpace(fields[0]), Username: fields[1], Password: fields[2], Enabled: false}
		n.Name = n.Address
		if len(n.Name) > 100 {
			n.Name = n.Name[:100]
		}
		if err := applyL2TPFields(&n, l2tpRequest{}); err != nil {
			return &httpError{http.StatusBadRequest, fmt.Sprintf("第 %d 行：%s", index+1, err.Error())}
		}
		nodes = append(nodes, n)
	}
	if len(nodes) == 0 {
		return &httpError{http.StatusBadRequest, "请输入至少一条 L2TP 连接"}
	}
	imported, skipped, err := s.Store.ImportL2TP(nodes)
	if err != nil {
		return err
	}
	writeJSON(w, map[string]any{"connections": s.l2tpConnections(imported), "imported": len(imported), "skipped": skipped})
	return nil
}
