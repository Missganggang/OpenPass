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
