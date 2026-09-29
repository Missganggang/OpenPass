package api

import (
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"openpass/internal/model"
)

func deviceRequest(s *Server, method, path, body string) *httptest.ResponseRecorder {
	w := httptest.NewRecorder()
	r := httptest.NewRequest(method, path, strings.NewReader(body))
	r.RemoteAddr = "192.0.2.25:12345"
	s.Handler().ServeHTTP(w, r)
	return w
}

func manualApplyFixture(t *testing.T) (*Server, *selfRuntime) {
	t.Helper()
	s, rt := selfFixture(t)
	v := s.Store.Settings()
	v.Enabled, v.AutoApply = false, false
	if err := s.Store.UpdateSettings(v); err != nil {
		t.Fatal(err)
	}
	return s, rt
}

func deviceByID(t *testing.T, s *Server, id string) model.Device {
	t.Helper()
	for _, d := range s.Store.Devices() {
		if d.ID == id {
			return d
		}
	}
	t.Fatalf("missing device %q", id)
	return model.Device{}
}

func TestExplicitBindingActivatesAndAppliesAcrossEndpoints(t *testing.T) {
	for _, tc := range []struct{ method, path, body string }{
		{"POST", "/api/devices/mine/bind", `{"mode":"proxy","node_id":"available"}`},
		{"POST", "/api/devices/mine/bind", `{"node_id":"available"}`},
		{"PATCH", "/api/devices/mine", `{"mode":"proxy","node_id":"available"}`},
		{"PATCH", "/api/devices/mine", `{"node_id":"available"}`},
		{"PUT", "/api/devices/mine", `{"mode":"proxy","node_id":"available"}`},
		{"POST", "/api/devices", `{"id":"mine","ip":"192.0.2.25","mac":"02:00:00:00:00:25","mode":"proxy","node_id":"available"}`},
		{"POST", "/api/self", `{"mode":"proxy","node_id":"available"}`},
	} {
		t.Run(tc.method+tc.path+tc.body, func(t *testing.T) {
			s, rt := manualApplyFixture(t)
			w := deviceRequest(s, tc.method, tc.path, tc.body)
			if w.Code != http.StatusOK {
				t.Fatalf("status=%d body=%s", w.Code, w.Body)
			}
			d := deviceByID(t, s, "mine")
			if !s.Store.Settings().Enabled || !rt.applied.Settings.Enabled || len(rt.applied.Devices) == 0 {
				t.Fatalf("proxy was saved without activation/application: %+v", s.Store.Settings())
			}
			if d.Mode != "proxy" || d.NodeID != "available" || d.DNS != "cloudflare" {
				t.Fatalf("wrong proxy policy: %+v", d)
			}
			if tc.path == "/api/self" && !strings.Contains(w.Body.String(), `"enabled":true`) {
				t.Fatalf("self response has stale activation: %s", w.Body)
			}
		})
	}
}

func TestInvalidBindingDoesNotMutateOrActivate(t *testing.T) {
	for _, tc := range []struct{ method, path string }{
		{"POST", "/api/devices/mine/bind"}, {"PATCH", "/api/devices/mine"},
		{"PUT", "/api/devices/mine"}, {"POST", "/api/self"},
	} {
		for _, node := range []string{"", "missing", "disabled"} {
			t.Run(tc.method+tc.path+node, func(t *testing.T) {
				s, rt := manualApplyFixture(t)
				w := deviceRequest(s, tc.method, tc.path, `{"mode":"proxy","node_id":"`+node+`"}`)
				if w.Code != http.StatusBadRequest {
					t.Fatalf("status=%d body=%s", w.Code, w.Body)
				}
				if d := deviceByID(t, s, "mine"); d.Mode != "direct" || d.NodeID != "" {
					t.Fatalf("invalid policy was persisted: %+v", d)
				}
				if s.Store.Settings().Enabled || len(rt.applied.Devices) != 0 {
					t.Fatal("invalid binding activated runtime")
				}
			})
		}
	}
	s, _ := manualApplyFixture(t)
	w := deviceRequest(s, "POST", "/api/devices", `{"id":"new","mode":"proxy","node_id":"missing"}`)
	if w.Code != http.StatusBadRequest || len(s.Store.Devices()) != 2 {
		t.Fatalf("invalid creation persisted: %d %s", w.Code, w.Body)
	}
}

func TestDeviceModeDefaultsAndExplicitDNS(t *testing.T) {
	for _, path := range []string{"/api/devices/mine", "/api/devices/mine/bind", "/api/self"} {
		t.Run(path, func(t *testing.T) {
			s, rt := manualApplyFixture(t)
			method := "POST"
			if path == "/api/devices/mine" {
				method = "PATCH"
			}
			for _, step := range []struct{ body, mode, dns string }{
				{`{"mode":"proxy","node_id":"available"}`, "proxy", "cloudflare"},
				{`{"mode":"direct"}`, "direct", "aliyun"},
				{`{"mode":"proxy","node_id":"available","dns":"tencent"}`, "proxy", "tencent"},
				{`{"mode":"direct","dns":"aliyun-secondary"}`, "direct", "aliyun-secondary"},
			} {
				w := deviceRequest(s, method, path, step.body)
				if w.Code != http.StatusOK {
					t.Fatalf("status=%d body=%s", w.Code, w.Body)
				}
				d := deviceByID(t, s, "mine")
				if d.Mode != step.mode || d.DNS != step.dns {
					t.Fatalf("policy=%+v expected=%+v", d, step)
				}
				for _, applied := range rt.applied.Devices {
					if applied.ID == d.ID && (applied.Mode != step.mode || applied.DNS != step.dns) {
						t.Fatalf("stale runtime policy: %+v", applied)
					}
				}
			}
		})
	}
}

func TestMetadataPreservesBindingAndDoesNotActivate(t *testing.T) {
	s, rt := manualApplyFixture(t)
	_, err := s.Store.UpdateDevice("mine", func(d *model.Device) { d.Mode, d.NodeID, d.DNS = "proxy", "available", "google" })
	if err != nil {
		t.Fatal(err)
	}
	w := deviceRequest(s, "PATCH", "/api/devices/mine", `{"remark":"  客户一号  ","hidden":false}`)
	if w.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", w.Code, w.Body)
	}
	d := deviceByID(t, s, "mine")
	if d.Remark != "客户一号" || d.Hidden || d.Mode != "proxy" || d.NodeID != "available" || d.DNS != "google" {
		t.Fatalf("metadata altered policy: %+v", d)
	}
	if s.Store.Settings().Enabled || len(rt.applied.Devices) != 0 {
		t.Fatal("metadata activated runtime")
	}
	w = deviceRequest(s, "PATCH", "/api/devices/mine", `{"remark":"`+strings.Repeat("字", 201)+`","hidden":true}`)
	if w.Code != http.StatusBadRequest {
		t.Fatalf("long remark accepted: %d %s", w.Code, w.Body)
	}
	if d := deviceByID(t, s, "mine"); d.Remark != "客户一号" || d.Hidden {
		t.Fatalf("invalid metadata partially saved: %+v", d)
	}
	if _, err := s.Store.UpdateNode("available", func(n *model.Node) error { n.Remark = "美国专用"; return nil }); err != nil {
		t.Fatal(err)
	}
	w = deviceRequest(s, "GET", "/api/self", "")
	var payload struct {
		Device model.Device `json:"device"`
		Nodes  []model.Node `json:"nodes"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &payload); err != nil {
		t.Fatal(err)
	}
	if payload.Device.Remark != "客户一号" || len(payload.Nodes) != 1 || payload.Nodes[0].Remark != "美国专用" {
		t.Fatalf("self remarks missing: %+v", payload)
	}
	for _, secret := range []string{"secret-never-return", "private-user", "private-uri"} {
		if strings.Contains(w.Body.String(), secret) {
			t.Fatal("self remark response leaked credentials")
		}
	}
}

func TestReleaseRemovesPolicyAndAppliesEvenWithAutoApplyOff(t *testing.T) {
	for _, online := range []bool{false, true} {
		s, rt := manualApplyFixture(t)
		_, err := s.Store.UpdateDevice("mine", func(d *model.Device) { d.Online, d.Mode, d.NodeID = online, "proxy", "available" })
		if err != nil {
			t.Fatal(err)
		}
		w := deviceRequest(s, "DELETE", "/api/devices/mine", "")
		if w.Code != http.StatusOK || !strings.Contains(w.Body.String(), `"released":true`) {
			t.Fatalf("release=%d %s", w.Code, w.Body)
		}
		for _, devices := range [][]model.Device{s.Store.Devices(), rt.applied.Devices} {
			if len(devices) != 1 || devices[0].ID != "other" {
				t.Fatalf("policy not removed: %+v", devices)
			}
		}
		w = deviceRequest(s, "DELETE", "/api/devices/mine", "")
		if w.Code != http.StatusNotFound {
			t.Fatalf("missing release=%d %s", w.Code, w.Body)
		}
	}
}

func TestDeviceActionsReportApplyFailure(t *testing.T) {
	for _, tc := range []struct{ method, path, body string }{
		{"PATCH", "/api/devices/mine", `{"mode":"proxy","node_id":"available"}`},
		{"POST", "/api/self", `{"mode":"proxy","node_id":"available"}`},
		{"DELETE", "/api/devices/mine", ""},
	} {
		s, rt := manualApplyFixture(t)
		rt.err = errors.New("kernel could not reload")
		w := deviceRequest(s, tc.method, tc.path, tc.body)
		if w.Code != http.StatusInternalServerError || !strings.Contains(w.Body.String(), rt.err.Error()) {
			t.Fatalf("application failure hidden: %d %s", w.Code, w.Body)
		}
	}
}
