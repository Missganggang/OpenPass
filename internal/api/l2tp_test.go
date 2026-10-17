package api

import (
	"encoding/json"
	"errors"
	"net/http"
	"path/filepath"
	"strings"
	"testing"

	"openpass/internal/l2tp"
	"openpass/internal/model"
	"openpass/internal/store"
)

type l2tpRuntime struct {
	applied     model.State
	applies     int
	reconnected string
	missing     []string
	err         error
}

func (rt *l2tpRuntime) Apply(st model.State) error {
	rt.applied, rt.applies = st, rt.applies+1
	return rt.err
}
func (rt *l2tpRuntime) L2TPDependencies() l2tp.Dependencies {
	return l2tp.Dependencies{Ready: len(rt.missing) == 0, Missing: rt.missing}
}
func (rt *l2tpRuntime) L2TPStatuses(ns []model.Node) map[string]l2tp.Status {
	out := map[string]l2tp.Status{}
	for _, n := range ns {
		st := l2tp.Status{State: "disconnected", Interface: "optest"}
		if n.Enabled {
			st.State, st.Connected, st.LocalIP = "connected", true, "192.0.2.7"
		}
		out[n.ID] = st
	}
	return out
}
func (rt *l2tpRuntime) ReconnectL2TP(id string) error { rt.reconnected = id; return rt.err }

func l2tpFixture(t *testing.T) (*Server, *l2tpRuntime, string) {
	t.Helper()
	path := filepath.Join(t.TempDir(), "state.json")
	st, err := store.New(path)
	if err != nil {
		t.Fatal(err)
	}
	s, rt := New(st), &l2tpRuntime{}
	s.Runtime = rt
	return s, rt, path
}

const l2tpCreateBody = `{"name":"Tokyo PPP","address":"vpn.example.net","username":"vpn user","password":"test-secret","remark":"客户专线"}`

func createTestL2TP(t *testing.T, s *Server) model.Node {
	t.Helper()
	w := deviceRequest(s, http.MethodPost, "/api/l2tp", l2tpCreateBody)
	if w.Code != http.StatusOK {
		t.Fatalf("create=%d %s", w.Code, w.Body)
	}
	if strings.Contains(w.Body.String(), "test-secret") {
		t.Fatal("create response exposes password")
	}
	return s.Store.Nodes()[0]
}

func TestL2TPCRUDPreservesCredentialsAndPersistsDefaults(t *testing.T) {
	s, rt, path := l2tpFixture(t)
	n := createTestL2TP(t, s)
	if n.Enabled || n.Type != "l2tp" || n.MTU != 1400 || n.Port != 1701 || rt.applies != 0 {
		t.Fatalf("unsafe create defaults: %+v applies=%d", n, rt.applies)
	}
	w := deviceRequest(s, http.MethodPatch, "/api/l2tp/"+n.ID, `{"remark":"新备注","password":"","mtu":1380}`)
	if w.Code != http.StatusOK || strings.Contains(w.Body.String(), "test-secret") {
		t.Fatalf("edit=%d %s", w.Code, w.Body)
	}
	reloaded, err := store.New(path)
	if err != nil {
		t.Fatal(err)
	}
	saved := reloaded.Nodes()[0]
	if saved.Username != "vpn user" || saved.Password != "test-secret" || saved.Remark != "新备注" || saved.MTU != 1380 {
		t.Fatalf("lost L2TP connection fields: %+v", saved)
	}
	for _, path := range []string{"/api/l2tp", "/api/l2tp/" + n.ID, "/api/nodes"} {
		w = deviceRequest(s, http.MethodGet, path, "")
		if w.Code != http.StatusOK || strings.Contains(w.Body.String(), "test-secret") {
			t.Fatalf("list %s leaks credential: %d %s", path, w.Code, w.Body)
		}
	}
	w = deviceRequest(s, http.MethodGet, "/api/l2tp", "")
	var response struct {
		Connections []struct {
			Status l2tp.Status `json:"status"`
		} `json:"connections"`
		Dependencies struct {
			Available bool     `json:"available"`
			Missing   []string `json:"missing"`
		} `json:"dependencies"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &response); err != nil || len(response.Connections) != 1 || response.Connections[0].Status.State != "disconnected" || !response.Dependencies.Available || response.Dependencies.Missing == nil {
		t.Fatalf("bad status payload: %s, err=%v", w.Body, err)
	}
}

func TestL2TPRejectsUnsafeInputAndGenericCreation(t *testing.T) {
	for _, patch := range []string{
		`{"address":"https://vpn.example.net"}`, `{"address":"vpn.example.net;reboot"}`,
		`{"address":"2001:db8::1"}`, `{"address":"999.1.2.3"}`, `{"address":"vpn..net"}`,
		`{"username":"bad\nname"}`, `{"password":"bad\"password"}`, `{"password":"bad\\password"}`,
		`{"mtu":1279}`, `{"mtu":1461}`, `{"enabled":true}`, `{"type":"socks5"}`,
	} {
		t.Run(patch, func(t *testing.T) {
			s, _, _ := l2tpFixture(t)
			n := createTestL2TP(t, s)
			w := deviceRequest(s, http.MethodPatch, "/api/l2tp/"+n.ID, patch)
			if w.Code != http.StatusBadRequest {
				t.Fatalf("invalid edit accepted: %d %s", w.Code, w.Body)
			}
			if got := s.Store.Nodes()[0]; got != n {
				t.Fatal("rejected edit mutated saved node")
			}
		})
	}
	s, _, _ := l2tpFixture(t)
	for _, tc := range []struct{ method, path, body string }{
		{"POST", "/api/nodes", `{"type":"l2tp","address":"vpn.example.net"}`},
		{"PUT", "/api/nodes/invented", `{"type":"L2TP","address":"vpn.example.net"}`},
		{"POST", "/api/l2tp", `{"name":"VPN","address":"vpn.example.net","username":"u","password":"p","enabled":true}`},
		{"POST", "/api/l2tp", `{"name":"VPN","address":"vpn.example.net","username":"u"}`},
	} {
		w := deviceRequest(s, tc.method, tc.path, tc.body)
		if w.Code != http.StatusBadRequest || len(s.Store.Nodes()) != 0 {
			t.Fatalf("generic creation bypassed validation: %d %s", w.Code, w.Body)
		}
	}
}

func TestL2TPConnectDisconnectAndReconnectPreserveBinding(t *testing.T) {
	s, rt, _ := l2tpFixture(t)
	n := createTestL2TP(t, s)
	w := deviceRequest(s, http.MethodPost, "/api/l2tp/"+n.ID+"/connect", "")
	if w.Code != http.StatusOK || !s.Store.Settings().Enabled || !rt.applied.Settings.Enabled || !rt.applied.Nodes[0].Enabled || !strings.Contains(w.Body.String(), `"connected":true`) {
		t.Fatalf("connect did not activate protections: %d %s", w.Code, w.Body)
	}
	if _, err := s.Store.UpsertDevice(model.Device{ID: "bound", MAC: "02:00:00:00:00:01", IP: "192.0.2.2", Mode: "proxy", NodeID: n.ID, Hidden: true}); err != nil {
		t.Fatal(err)
	}
	w = deviceRequest(s, http.MethodPost, "/api/l2tp/"+n.ID+"/disconnect", "")
	if w.Code != http.StatusOK || s.Store.Nodes()[0].Enabled || rt.applied.Nodes[0].Enabled || !rt.applied.Settings.Enabled || s.Store.Devices()[0].NodeID != n.ID {
		t.Fatalf("disconnect lost fail-closed binding: %d %s", w.Code, w.Body)
	}
	w = deviceRequest(s, http.MethodPost, "/api/l2tp/"+n.ID+"/reconnect", "")
	if w.Code != http.StatusOK || rt.reconnected != n.ID || !s.Store.Nodes()[0].Enabled || rt.applies != 3 {
		t.Fatalf("reconnect=%d %s", w.Code, w.Body)
	}
	for _, path := range []string{"/api/l2tp/" + n.ID, "/api/nodes/" + n.ID} {
		w = deviceRequest(s, http.MethodDelete, path, "")
		if w.Code != http.StatusConflict || !strings.Contains(w.Body.String(), "请先解绑") || len(s.Store.Nodes()) != 1 {
			t.Fatalf("bound delete=%d %s", w.Code, w.Body)
		}
	}
	if err := s.Store.DeleteDevice("bound"); err != nil {
		t.Fatal(err)
	}
	w = deviceRequest(s, http.MethodDelete, "/api/l2tp/"+n.ID, "")
	if w.Code != http.StatusOK || len(s.Store.Nodes()) != 0 || len(rt.applied.Nodes) != 0 {
		t.Fatalf("unbound delete=%d %s", w.Code, w.Body)
	}
}

func TestL2TPMissingDependenciesAndApplyFailureAreReported(t *testing.T) {
	s, rt, _ := l2tpFixture(t)
	n := createTestL2TP(t, s)
	rt.missing = []string{"xl2tpd"}
	w := deviceRequest(s, "POST", "/api/l2tp/"+n.ID+"/connect", "")
	if w.Code != http.StatusConflict || s.Store.Nodes()[0].Enabled || s.Store.Settings().Enabled || rt.applies != 0 {
		t.Fatalf("missing dependency activated state: %d %s", w.Code, w.Body)
	}
	rt.missing, rt.err = nil, errors.New("network setup failed")
	w = deviceRequest(s, "POST", "/api/l2tp/"+n.ID+"/connect", "")
	if w.Code != http.StatusInternalServerError || !strings.Contains(w.Body.String(), "应用失败") {
		t.Fatalf("apply failure hidden: %d %s", w.Code, w.Body)
	}
	if !s.Store.Nodes()[0].Enabled || !s.Store.Settings().Enabled {
		t.Fatal("failed apply must retain desired binding protections for retries")
	}
}

func TestL2TPExportAndProbeDoNotUseSingBox(t *testing.T) {
	s, _, _ := l2tpFixture(t)
	n := createTestL2TP(t, s)
	w := deviceRequest(s, "POST", "/api/nodes/"+n.ID+"/test", `{"type":"url"}`)
	if w.Code != http.StatusBadRequest || !strings.Contains(w.Body.String(), "L2TP 页面") {
		t.Fatalf("L2TP entered sing-box probe: %d %s", w.Code, w.Body)
	}
	w = deviceRequest(s, "GET", "/api/nodes/"+n.ID+"/export", "")
	if w.Code != http.StatusBadRequest || !strings.Contains(w.Body.String(), "JSON") {
		t.Fatalf("L2TP share link=%d %s", w.Code, w.Body)
	}
	w = deviceRequest(s, "GET", "/api/nodes/export", "")
	if w.Code != http.StatusOK || strings.Contains(w.Body.String(), "test-secret") {
		t.Fatalf("bulk URI export failed on L2TP: %d %s", w.Code, w.Body)
	}
	w = deviceRequest(s, "GET", "/api/nodes/export?format=json", "")
	if w.Code != http.StatusOK || strings.TrimSpace(w.Body.String()) != "[]" {
		t.Fatalf("ordinary bulk JSON export includes a hidden L2TP connection: %d %s", w.Code, w.Body)
	}
	w = deviceRequest(s, "GET", "/api/nodes/"+n.ID+"/export?format=json", "")
	if w.Code != http.StatusOK || !strings.Contains(w.Body.String(), "test-secret") {
		t.Fatalf("explicit JSON backup lost password: %d %s", w.Code, w.Body)
	}
}

func TestL2TPSelfServiceListsOnlyEnabledConnectionsWithoutCredentials(t *testing.T) {
	t.Setenv("PATH", t.TempDir())
	s, _, _ := l2tpFixture(t)
	n := createTestL2TP(t, s)
	w := deviceRequest(s, "GET", "/api/self", "")
	if w.Code != http.StatusOK || strings.Contains(w.Body.String(), n.ID) {
		t.Fatalf("disabled L2TP selectable: %d %s", w.Code, w.Body)
	}
	w = deviceRequest(s, "POST", "/api/l2tp/"+n.ID+"/connect", "")
	if w.Code != http.StatusOK {
		t.Fatal(w.Body)
	}
	w = deviceRequest(s, "GET", "/api/self", "")
	if w.Code != http.StatusOK || !strings.Contains(w.Body.String(), n.ID) || !strings.Contains(w.Body.String(), "客户专线") || !strings.Contains(w.Body.String(), `"type":"l2tp"`) || strings.Contains(w.Body.String(), "test-secret") || strings.Contains(w.Body.String(), "vpn user") {
		t.Fatalf("self-service L2TP metadata wrong: %d %s", w.Code, w.Body)
	}
}
