package api

import (
	"encoding/json"
	"net/http"
	"strings"
	"testing"

	"openpass/internal/model"
)

func TestNodeDeleteRejectsAllDeviceBindingsUntilUnbound(t *testing.T) {
	for _, tc := range []struct {
		name           string
		online, hidden bool
	}{
		{"online", true, false}, {"offline", false, false}, {"hidden", false, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s, rt := selfFixture(t)
			if _, err := s.Store.UpdateDevice("mine", func(d *model.Device) {
				d.Mode, d.NodeID, d.Online, d.Hidden = "proxy", "available", tc.online, tc.hidden
			}); err != nil {
				t.Fatal(err)
			}
			w := deviceRequest(s, "DELETE", "/api/nodes/available", "")
			if w.Code != http.StatusConflict {
				t.Fatalf("delete=%d %s", w.Code, w.Body)
			}
			var response struct {
				Error string         `json:"error"`
				Bound []model.Device `json:"bound_devices"`
			}
			if err := json.Unmarshal(w.Body.Bytes(), &response); err != nil {
				t.Fatal(err)
			}
			if !strings.Contains(response.Error, "请先解绑") || len(response.Bound) != 1 || response.Bound[0].ID != "mine" {
				t.Fatalf("missing binding guidance: %s", w.Body)
			}
			if len(s.Store.Nodes()) != 2 || len(rt.applied.Nodes) != 0 {
				t.Fatal("rejected deletion changed node/runtime")
			}
			if deviceByID(t, s, "mine").NodeID != "available" {
				t.Fatal("deletion changed device binding")
			}
			if strings.Contains(w.Body.String(), "secret-never-return") {
				t.Fatal("conflict exposes node credentials")
			}
			w = deviceRequest(s, "PATCH", "/api/devices/mine", `{"mode":"direct"}`)
			if w.Code != 200 {
				t.Fatalf("unbind=%d %s", w.Code, w.Body)
			}
			w = deviceRequest(s, "DELETE", "/api/nodes/available", "")
			if w.Code != 200 || len(s.Store.Nodes()) != 1 || len(rt.applied.Nodes) != 1 {
				t.Fatalf("unbound delete=%d %s", w.Code, w.Body)
			}
		})
	}
}

func TestDeleteLastNodeReturnsEmptyArrayAndDevicesRemainReadable(t *testing.T) {
	t.Setenv("PATH", t.TempDir()) // Keep the host's neighbour table out of this fixture.
	s, _ := selfFixture(t)
	for _, id := range []string{"available", "disabled"} {
		w := deviceRequest(s, "DELETE", "/api/nodes/"+id, "")
		if w.Code != 200 {
			t.Fatalf("delete=%d %s", w.Code, w.Body)
		}
	}
	w := deviceRequest(s, "GET", "/api/nodes", "")
	if w.Code != 200 || strings.TrimSpace(w.Body.String()) != "[]" {
		t.Fatalf("empty node list=%d %s", w.Code, w.Body)
	}
	w = deviceRequest(s, "GET", "/api/devices?hidden=all", "")
	var devices []model.Device
	if w.Code != 200 || json.Unmarshal(w.Body.Bytes(), &devices) != nil || len(devices) != 2 {
		t.Fatalf("devices=%d %s", w.Code, w.Body)
	}
	w = deviceRequest(s, "DELETE", "/api/nodes/available", "")
	if w.Code != http.StatusNotFound {
		t.Fatalf("missing node status=%d", w.Code)
	}
}
