package api

import (
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"

	"openpass/internal/model"
	"openpass/internal/store"
)

type selfRuntime struct {
	applied model.State
	err     error
}

func (rt *selfRuntime) Apply(st model.State) error { rt.applied = st; return rt.err }

func selfFixture(t *testing.T) (*Server, *selfRuntime) {
	t.Helper()
	st, err := store.New(filepath.Join(t.TempDir(), "state.json"))
	if err != nil {
		t.Fatal(err)
	}
	for _, d := range []model.Device{
		{ID: "mine", IP: "192.0.2.25", MAC: "02:00:00:00:00:25", Mode: "direct", Hidden: true},
		{ID: "other", IP: "192.0.2.26", MAC: "02:00:00:00:00:26", Mode: "direct"},
	} {
		if _, err := st.UpsertDevice(d); err != nil {
			t.Fatal(err)
		}
	}
	for _, n := range []model.Node{
		{ID: "available", Type: "socks5", Enabled: true, Password: "secret-never-return", UUID: "private-user", URI: "private-uri"},
		{ID: "disabled", Type: "vless", Enabled: false},
	} {
		if _, err := st.UpsertNode(n); err != nil {
			t.Fatal(err)
		}
	}
	s := New(st)
	rt := &selfRuntime{}
	s.Runtime = rt
	return s, rt
}

func selfRequest(s *Server, method, body string) *httptest.ResponseRecorder {
	r := httptest.NewRequest(method, "/api/self", strings.NewReader(body))
	r.RemoteAddr = "[::ffff:192.0.2.25]:53123"
	r.Header.Set("X-Forwarded-For", "192.0.2.26")
	w := httptest.NewRecorder()
	s.Handler().ServeHTTP(w, r)
	return w
}

func TestSelfReportsPeerIncludingHiddenDevice(t *testing.T) {
	s, _ := selfFixture(t)
	w := selfRequest(s, http.MethodGet, "")
	if w.Code != 200 {
		t.Fatalf("status=%d body=%s", w.Code, w.Body)
	}
	var v struct {
		Device  model.Device `json:"device"`
		Nodes   []model.Node `json:"nodes"`
		CanBind bool         `json:"can_bind"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &v); err != nil {
		t.Fatal(err)
	}
	if v.Device.ID != "mine" || v.Device.IP != "192.0.2.25" || v.Device.MAC != "02:00:00:00:00:25" || !v.CanBind {
		t.Fatalf("wrong identity: %+v", v)
	}
	if len(v.Nodes) != 1 || v.Nodes[0].ID != "available" {
		t.Fatalf("nodes=%+v", v.Nodes)
	}
	for _, secret := range []string{"secret-never-return", "private-user", "private-uri"} {
		if strings.Contains(w.Body.String(), secret) {
			t.Fatal("self response exposes credential")
		}
	}
}

func TestSelfBindingReturnsJSONAfterApplyAndClearsOnDirect(t *testing.T) {
	s, rt := selfFixture(t)
	for _, body := range []string{`{"mode":"proxy","node_id":"available","id":"other","ip":"192.0.2.26"}`, `{"mode":"direct"}`} {
		w := selfRequest(s, http.MethodPost, body)
		if w.Code != 200 || !json.Valid(w.Body.Bytes()) {
			t.Fatalf("status=%d body=%s", w.Code, w.Body)
		}
	}
	for _, d := range rt.applied.Devices {
		if d.Mode != "direct" || d.NodeID != "" {
			t.Fatalf("binding not cleared/other device changed: %+v", d)
		}
	}
}

func TestSelfRejectsInvalidNodeAndDisabledService(t *testing.T) {
	s, _ := selfFixture(t)
	for _, id := range []string{"missing", "disabled", ""} {
		w := selfRequest(s, http.MethodPost, `{"mode":"proxy","node_id":"`+id+`"}`)
		if w.Code != 400 {
			t.Fatalf("invalid node status %d", w.Code)
		}
	}
	settings := s.Store.Settings()
	settings.SelfServiceEnabled = false
	if err := s.Store.UpdateSettings(settings); err != nil {
		t.Fatal(err)
	}
	for _, method := range []string{http.MethodGet, http.MethodPost} {
		w := selfRequest(s, method, `{"mode":"direct"}`)
		if w.Code != 403 {
			t.Fatalf("disabled status=%d", w.Code)
		}
	}
}

func TestSelfReportsApplyErrorAndUnknownPeer(t *testing.T) {
	s, rt := selfFixture(t)
	rt.err = errors.New("kernel rejected config")
	w := selfRequest(s, http.MethodPost, `{"mode":"proxy","node_id":"available"}`)
	if w.Code == 200 || !strings.Contains(w.Body.String(), "kernel rejected config") {
		t.Fatalf("missing apply error: %d %s", w.Code, w.Body)
	}
	r := httptest.NewRequest(http.MethodPost, "/api/self", strings.NewReader(`{"mode":"direct"}`))
	r.RemoteAddr = "192.0.2.99:1234"
	w = httptest.NewRecorder()
	s.Handler().ServeHTTP(w, r)
	if w.Code != 409 {
		t.Fatalf("unknown peer=%d", w.Code)
	}
}
