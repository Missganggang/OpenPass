package api

import (
	"encoding/json"
	"errors"
	"net/http"
	"strings"
	"testing"

	"openpass/internal/model"
	"openpass/internal/store"
)

func TestL2TPModuleSwitchRetainsIntentAndStopsPolling(t *testing.T) {
	s, rt, path := l2tpFixture(t)
	n := createTestL2TP(t, s)
	if w := deviceRequest(s, "POST", "/api/l2tp/"+n.ID+"/connect", ""); w.Code != http.StatusOK {
		t.Fatal(w.Body)
	}
	if _, err := s.Store.UpsertDevice(model.Device{ID: "bound", Mode: "proxy", NodeID: n.ID}); err != nil {
		t.Fatal(err)
	}
	settings := s.Store.Settings()
	settings.AutoApply = false
	if err := s.Store.UpdateSettings(settings); err != nil {
		t.Fatal(err)
	}
	w := deviceRequest(s, "PATCH", "/api/l2tp/settings", `{"enabled":false}`)
	if w.Code != http.StatusOK || rt.applies != 2 || !rt.applied.Settings.L2TPDisabled || !rt.applied.Nodes[0].Enabled || s.Store.Devices()[0].NodeID != n.ID {
		t.Fatalf("closing module lost intent, bindings or immediate application: %d %s", w.Code, w.Body)
	}
	rt.depReads, rt.statusReads = 0, 0
	for _, path := range []string{"/api/l2tp", "/api/l2tp/" + n.ID, "/api/l2tp/settings"} {
		w = deviceRequest(s, "GET", path, "")
		if w.Code != http.StatusOK || strings.Contains(w.Body.String(), "test-secret") {
			t.Fatalf("disabled response=%d %s", w.Code, w.Body)
		}
		if path != "/api/l2tp/settings" && !strings.Contains(w.Body.String(), `"state":"disabled"`) {
			t.Fatalf("disabled connection reported online: %s", w.Body)
		}
	}
	w = deviceRequest(s, "GET", "/api/l2tp", "")
	if !strings.Contains(w.Body.String(), `"dependencies":null`) || !strings.Contains(w.Body.String(), `"enabled":false`) || rt.depReads != 0 || rt.statusReads != 0 {
		t.Fatalf("disabled module still probes the runtime: %d/%d %s", rt.depReads, rt.statusReads, w.Body)
	}
	for _, action := range []string{"connect", "reconnect"} {
		w = deviceRequest(s, "POST", "/api/l2tp/"+n.ID+"/"+action, "")
		if w.Code != http.StatusConflict || rt.applies != 2 || rt.depReads != 0 || rt.statusReads != 0 {
			t.Fatalf("disabled action=%d %s", w.Code, w.Body)
		}
	}
	reloaded, err := store.New(path)
	if err != nil || !reloaded.Settings().L2TPDisabled || !reloaded.Nodes()[0].Enabled {
		t.Fatalf("switch not persisted: %v", err)
	}
	settings = s.Store.Settings()
	settings.Enabled = false
	if err := s.Store.UpdateSettings(settings); err != nil {
		t.Fatal(err)
	}
	w = deviceRequest(s, "PATCH", "/api/l2tp/settings", `{"enabled":true}`)
	if w.Code != http.StatusOK || rt.applied.Settings.L2TPDisabled || !rt.applied.Settings.Enabled || !rt.applied.Nodes[0].Enabled || rt.applies != 3 {
		t.Fatalf("opening module failed to restore intent and protection: %d %s", w.Code, w.Body)
	}
}

func TestL2TPSettingsRejectsMalformedInputAndGenericBypass(t *testing.T) {
	s, rt, _ := l2tpFixture(t)
	for _, body := range []string{`{}`, `{"enabled":null}`, `{"enabled":"false"}`, `{"enabled":false,"extra":1}`, `{"enabled":false} {}`} {
		w := deviceRequest(s, "PATCH", "/api/l2tp/settings", body)
		if w.Code != http.StatusBadRequest || s.Store.Settings().L2TPDisabled || rt.applies != 0 {
			t.Fatalf("invalid switch accepted: %d %s", w.Code, w.Body)
		}
	}
	for _, method := range []string{"PUT", "PATCH"} {
		w := deviceRequest(s, method, "/api/settings", `{"l2tp_disabled":true,"auto_apply":false}`)
		if w.Code != http.StatusBadRequest || s.Store.Settings().L2TPDisabled || rt.applies != 0 {
			t.Fatalf("general settings bypass=%d %s", w.Code, w.Body)
		}
	}
	w := deviceRequest(s, "PATCH", "/api/settings", `{"l2tp_disabled":false,"auto_apply":false}`)
	if w.Code != http.StatusOK {
		t.Fatalf("unchanged full-form module field rejected: %d %s", w.Code, w.Body)
	}
}

func TestL2TPApplyFailureIsReadableAfterStartupAndRetryClearsIt(t *testing.T) {
	s, rt, _ := l2tpFixture(t)
	if _, err := s.Store.SetL2TPEnabled(false); err != nil {
		t.Fatal(err)
	}
	// Startup calls Runtime.Apply directly, before any module PATCH request.
	rt.err = errors.New("无法断开 L2TP 接口")
	if err := rt.Apply(s.Store.State()); err == nil {
		t.Fatal("fixture should fail startup restoration")
	}
	for _, path := range []string{"/api/l2tp", "/api/l2tp/settings"} {
		w := deviceRequest(s, "GET", path, "")
		var result map[string]any
		if err := json.Unmarshal(w.Body.Bytes(), &result); err != nil || w.Code != http.StatusOK || result["enabled"] != false || result["apply_error"] != rt.err.Error() {
			t.Fatalf("startup apply failure hidden: %s HTTP %d decode %v", path, w.Code, err)
		}
	}
	if rt.depReads != 0 || rt.statusReads != 0 {
		t.Fatal("reading the saved failure polled the disabled module")
	}
	w := deviceRequest(s, "PATCH", "/api/l2tp/settings", `{"enabled":false}`)
	var failure map[string]any
	if err := json.Unmarshal(w.Body.Bytes(), &failure); err != nil || w.Code != http.StatusInternalServerError || failure["enabled"] != false || failure["apply_error"] != rt.err.Error() {
		t.Fatalf("failed PATCH lost desired state or error: HTTP %d decode %v", w.Code, err)
	}
	if message, _ := failure["error"].(string); !strings.Contains(message, "配置已保存，但应用失败") {
		t.Fatalf("failed PATCH changed the API error contract: %v", failure["error"])
	}
	rt.err = nil
	w = deviceRequest(s, "PATCH", "/api/l2tp/settings", `{"enabled":false}`)
	var success map[string]any
	if err := json.Unmarshal(w.Body.Bytes(), &success); err != nil || w.Code != http.StatusOK || success["enabled"] != false || success["apply_error"] != "" {
		t.Fatalf("successful retry did not clear old error: HTTP %d decode %v", w.Code, err)
	}
	for _, path := range []string{"/api/l2tp", "/api/l2tp/settings"} {
		w = deviceRequest(s, "GET", path, "")
		var result map[string]any
		if err := json.Unmarshal(w.Body.Bytes(), &result); err != nil || result["apply_error"] != "" {
			t.Fatalf("GET must explicitly clear the error after retry: %s decode %v", path, err)
		}
	}
}

func TestL2TPDisabledModuleRejectsNewBindingsButRetainsExistingPolicies(t *testing.T) {
	t.Setenv("PATH", t.TempDir())
	s, _, _ := l2tpFixture(t)
	n := createTestL2TP(t, s)
	n.Enabled = true
	if _, err := s.Store.UpsertNode(n); err != nil {
		t.Fatal(err)
	}
	for _, device := range []model.Device{
		{ID: "mine", IP: "192.0.2.25", MAC: "02:00:00:00:00:25", Mode: "direct"},
		{ID: "bound", IP: "192.0.2.26", MAC: "02:00:00:00:00:26", Mode: "proxy", NodeID: n.ID},
	} {
		if _, err := s.Store.UpsertDevice(device); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := s.Store.SetL2TPEnabled(false); err != nil {
		t.Fatal(err)
	}
	for _, endpoint := range []struct{ method, path string }{
		{"POST", "/api/devices/mine/bind"}, {"PATCH", "/api/devices/mine"}, {"POST", "/api/self"},
	} {
		w := deviceRequest(s, endpoint.method, endpoint.path, `{"mode":"proxy","node_id":"`+n.ID+`"}`)
		if w.Code != http.StatusBadRequest || deviceByID(t, s, "mine").Mode != "direct" {
			t.Fatalf("disabled module newly bound: %d %s", w.Code, w.Body)
		}
	}
	for _, body := range []string{`{"remark":"保留"}`, `{"dns":"aliyun"}`, `{"mode":"proxy","node_id":"` + n.ID + `"}`} {
		w := deviceRequest(s, "PATCH", "/api/devices/bound", body)
		if w.Code != http.StatusOK || deviceByID(t, s, "bound").NodeID != n.ID {
			t.Fatalf("existing disabled module binding lost: %d %s", w.Code, w.Body)
		}
	}
	w := deviceRequest(s, "GET", "/api/self", "")
	if w.Code != http.StatusOK || strings.Contains(w.Body.String(), n.ID) || !strings.Contains(w.Body.String(), `"l2tp_enabled":false`) {
		t.Fatalf("disabled module connection selectable: %d %s", w.Code, w.Body)
	}
	mine := deviceByID(t, s, "mine")
	mine.Mode, mine.NodeID = "proxy", n.ID
	if _, err := s.Store.UpsertDevice(mine); err != nil {
		t.Fatal(err)
	}
	w = deviceRequest(s, "GET", "/api/self", "")
	var self struct {
		Nodes     []model.Node `json:"nodes"`
		BoundNode model.Node   `json:"bound_node"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &self); err != nil || w.Code != http.StatusOK || len(self.Nodes) != 0 || self.BoundNode.ID != n.ID || self.BoundNode.Name != n.Name || self.BoundNode.Remark != n.Remark || self.BoundNode.Password != "" || self.BoundNode.Username != "" {
		t.Fatalf("disabled bound node metadata missing or unredacted: HTTP %d decode %v", w.Code, err)
	}
}

func TestL2TPCredentialsRequireExplicitPostAndRemainAvailableWhenDisabled(t *testing.T) {
	s, _, _ := l2tpFixture(t)
	n := createTestL2TP(t, s)
	if _, err := s.Store.SetL2TPEnabled(false); err != nil {
		t.Fatal(err)
	}
	w := deviceRequest(s, "GET", "/api/l2tp/"+n.ID+"/credentials", "")
	if w.Code != http.StatusMethodNotAllowed || strings.Contains(w.Body.String(), n.Password) {
		t.Fatalf("credentials returned by ordinary GET: %d %s", w.Code, w.Body)
	}
	w = deviceRequest(s, "POST", "/api/l2tp/"+n.ID+"/credentials", "")
	var credentials map[string]string
	if err := json.Unmarshal(w.Body.Bytes(), &credentials); err != nil || w.Code != http.StatusOK || credentials["address"] != n.Address || credentials["username"] != n.Username || credentials["password"] != n.Password || w.Header().Get("Cache-Control") != "no-store" {
		t.Fatalf("explicit credential request failed, HTTP %d, decode %v", w.Code, err)
	}
}

func l2tpImportBody(t *testing.T, text string) string {
	t.Helper()
	b, err := json.Marshal(map[string]string{"text": text})
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

func TestL2TPBulkImportIsAtomicRedactedAndDeduplicated(t *testing.T) {
	s, rt, path := l2tpFixture(t)
	n := createTestL2TP(t, s)
	text := n.Address + ":" + n.Username + ":" + n.Password + "\r\n\nVPN2.example.net:two:colon:secret\nVPN2.EXAMPLE.NET:two:colon:secret\nvpn2.example.net:other:another-secret"
	w := deviceRequest(s, "POST", "/api/l2tp/import", l2tpImportBody(t, text))
	var body struct {
		Connections []model.Node `json:"connections"`
		Imported    int          `json:"imported"`
		Skipped     int          `json:"skipped"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &body); err != nil || w.Code != http.StatusOK || body.Imported != 2 || body.Skipped != 2 || len(body.Connections) != 2 || rt.applies != 0 {
		t.Fatalf("bulk import result=%d %s decode=%v", w.Code, w.Body, err)
	}
	if strings.Contains(w.Body.String(), "secret") {
		t.Fatal("import response exposed passwords")
	}
	reloaded, err := store.New(path)
	if err != nil || len(reloaded.Nodes()) != 3 || reloaded.Settings().Enabled {
		t.Fatalf("import not persisted or activated globally: %v", err)
	}
	for _, n := range reloaded.Nodes()[1:] {
		if n.Enabled || n.Type != "l2tp" || n.MTU != 1400 || n.Port != 1701 {
			t.Fatal("bulk connection has unsafe defaults")
		}
	}
	if reloaded.Nodes()[1].Password != "colon:secret" {
		t.Fatal("password colon was not retained")
	}
	before := s.Store.Nodes()
	w = deviceRequest(s, "POST", "/api/l2tp/import", l2tpImportBody(t, "valid.example.net:user:valid-secret\n\nbad-address/path:user:private-password"))
	if w.Code != http.StatusBadRequest || !strings.Contains(w.Body.String(), "第 3 行") || strings.Contains(w.Body.String(), "private-password") || strings.Contains(w.Body.String(), "bad-address") || len(s.Store.Nodes()) != len(before) {
		t.Fatalf("invalid batch was partly saved or error exposed input: %d %s", w.Code, w.Body)
	}
}

func TestL2TPImportLimitsAndValidation(t *testing.T) {
	s, _, _ := l2tpFixture(t)
	for _, text := range []string{"", "\n \r\n", "host:user", "host:user:\"secret", strings.Repeat("host:user:secret\n", 501)} {
		w := deviceRequest(s, "POST", "/api/l2tp/import", l2tpImportBody(t, text))
		if w.Code != http.StatusBadRequest || len(s.Store.Nodes()) != 0 {
			t.Fatalf("invalid import=%d %s", w.Code, w.Body)
		}
	}
	w := deviceRequest(s, "POST", "/api/l2tp/import", l2tpImportBody(t, strings.Repeat("x", maxL2TPImportBytes)))
	if w.Code != http.StatusBadRequest || !strings.Contains(w.Body.String(), "1 MiB") || len(s.Store.Nodes()) != 0 {
		t.Fatalf("oversized request=%d %s", w.Code, w.Body)
	}
}
