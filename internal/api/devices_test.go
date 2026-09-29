package api

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"openpass/internal/model"
)

func TestAdminNonProxyModeClearsBinding(t *testing.T) {
	for _, tc := range []struct {
		name, method, path, body, mode string
	}{
		{"direct with omitted node", http.MethodPatch, "/api/devices/mine", `{"mode":"direct"}`, "direct"},
		{"direct with null node", http.MethodPatch, "/api/devices/mine", `{"mode":"direct","node_id":null}`, "direct"},
		{"blocked with stale node", http.MethodPut, "/api/devices/mine", `{"mode":"blocked","node_id":"available"}`, "blocked"},
		{"bind direct with stale node", http.MethodPost, "/api/devices/mine/bind", `{"mode":"direct","node_id":"available"}`, "direct"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s, rt := selfFixture(t)
			if _, err := s.Store.UpdateDevice("mine", func(d *model.Device) {
				d.Mode, d.NodeID = "proxy", "available"
			}); err != nil {
				t.Fatal(err)
			}
			w := httptest.NewRecorder()
			s.Handler().ServeHTTP(w, httptest.NewRequest(tc.method, tc.path, strings.NewReader(tc.body)))
			if w.Code != http.StatusOK {
				t.Fatalf("status=%d body=%s", w.Code, w.Body)
			}
			var device model.Device
			if err := json.Unmarshal(w.Body.Bytes(), &device); err != nil {
				t.Fatal(err)
			}
			if device.Mode != tc.mode || device.NodeID != "" {
				t.Fatalf("stale binding in response: %+v", device)
			}
			for _, d := range rt.applied.Devices {
				if d.ID == "mine" && (d.Mode != tc.mode || d.NodeID != "") {
					t.Fatalf("stale binding in applied configuration: %+v", d)
				}
			}
			w = selfRequest(s, http.MethodGet, "")
			var self struct {
				Device model.Device `json:"device"`
			}
			if err := json.Unmarshal(w.Body.Bytes(), &self); err != nil {
				t.Fatal(err)
			}
			if self.Device.Mode != tc.mode || self.Device.NodeID != "" {
				t.Fatalf("stale binding in self page: %+v", self.Device)
			}
		})
	}
}

func TestNodeRemarkPatchAndExport(t *testing.T) {
	s, _ := selfFixture(t)
	if _, err := s.Store.UpsertNode(model.Node{ID: "node1", Name: "Test", Type: "socks5", Address: "127.0.0.1", Port: 1080, UUID: "u", Password: "p", Enabled: true}); err != nil {
		t.Fatal(err)
	}
	_ = s.Store.DeleteNode("disabled")
	_ = s.Store.DeleteNode("available")
	if _, err := s.Store.UpsertNode(model.Node{ID: "disabled", Name: "Disabled", Type: "socks5", Address: "127.0.0.2", Port: 1081, Enabled: false}); err != nil {
		t.Fatal(err)
	}
	w := httptest.NewRecorder()
	s.Handler().ServeHTTP(w, httptest.NewRequest(http.MethodPatch, "/api/nodes/node1", strings.NewReader(`{"remark":"海外主节点"}`)))
	if w.Code != http.StatusOK || !strings.Contains(w.Body.String(), "海外主节点") {
		t.Fatalf("remark patch status=%d body=%s", w.Code, w.Body)
	}
	w = httptest.NewRecorder()
	s.Handler().ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/api/nodes/export?format=uri", nil))
	if w.Code != http.StatusOK || w.Header().Get("Content-Disposition") == "" || !strings.Contains(w.Body.String(), "socks5://u:p@127.0.0.1:1080") || !strings.Contains(w.Body.String(), "socks5://127.0.0.2:1081") {
		t.Fatalf("URI export status=%d body=%s", w.Code, w.Body)
	}
	w = httptest.NewRecorder()
	s.Handler().ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/api/nodes/node1/export?format=uri", nil))
	if w.Code != http.StatusOK || w.Header().Get("Content-Disposition") == "" || strings.TrimSpace(w.Body.String()) != "socks5://u:p@127.0.0.1:1080#Test" {
		t.Fatalf("single URI export status=%d disposition=%q body=%s", w.Code, w.Header().Get("Content-Disposition"), w.Body)
	}
	w = httptest.NewRecorder()
	s.Handler().ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/api/nodes/node1/export?format=json", nil))
	if w.Code != http.StatusOK || !strings.Contains(w.Body.String(), `"id":"node1"`) || !strings.Contains(w.Body.String(), `"password":"p"`) {
		t.Fatalf("single JSON export status=%d body=%s", w.Code, w.Body)
	}
	w = httptest.NewRecorder()
	s.Handler().ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/api/nodes/missing/export?format=uri", nil))
	if w.Code != http.StatusNotFound {
		t.Fatalf("missing single export status=%d body=%s", w.Code, w.Body)
	}
	w = httptest.NewRecorder()
	s.Handler().ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/api/nodes/export?format=json", nil))
	if w.Code != http.StatusOK || !strings.Contains(w.Body.String(), "海外主节点") {
		t.Fatalf("JSON export status=%d body=%s", w.Code, w.Body)
	}
	var exported []model.Node
	err := json.Unmarshal(w.Body.Bytes(), &exported)
	var exportedNode *model.Node
	for i := range exported {
		if exported[i].ID == "node1" {
			exportedNode = &exported[i]
			break
		}
	}
	if err != nil || len(exported) != 2 || exportedNode == nil || exportedNode.Password != "p" {
		t.Fatalf("JSON export lost credentials: %#v err=%v", exported, err)
	}
	ns := s.Store.Nodes()
	if len(ns) != 2 || ns[0].Remark != "海外主节点" || ns[0].Password != "p" {
		t.Fatalf("PATCH did not preserve node fields: %#v", ns)
	}
}
