package store

import (
	"os"
	"path/filepath"
	"testing"

	"openpass/internal/model"
)

func TestDevicePolicySaveAndReleaseRollbackOnDiskFailure(t *testing.T) {
	path := filepath.Join(t.TempDir(), "state.json")
	s, err := New(path)
	if err != nil {
		t.Fatal(err)
	}
	d, err := s.UpsertDevice(model.Device{ID: "client", MAC: "02:00:00:00:00:01", Mode: "direct", Remark: "保留备注"})
	if err != nil {
		t.Fatal(err)
	}
	// An occupied temporary-file path simulates a disk write failure without
	// relying on Unix permissions, so this also runs on Windows.
	if err := os.Mkdir(path+".tmp", 0700); err != nil {
		t.Fatal(err)
	}
	d.Mode, d.NodeID = "proxy", "node"
	if _, err := s.UpsertDevicePolicy(d, true); err == nil {
		t.Fatal("expected save failure")
	}
	if s.Settings().Enabled {
		t.Fatal("failed device write activated global protection")
	}
	if ds := s.Devices(); len(ds) != 1 || ds[0].Mode != "direct" || ds[0].Remark != "保留备注" {
		t.Fatalf("failed write changed memory: %+v", ds)
	}
	if err := s.DeleteDevice("client"); err == nil {
		t.Fatal("expected release save failure")
	}
	if ds := s.Devices(); len(ds) != 1 || ds[0].ID != "client" {
		t.Fatalf("failed release removed device: %+v", ds)
	}
	reloaded, err := New(path)
	if err != nil {
		t.Fatal(err)
	}
	if ds := reloaded.Devices(); len(ds) != 1 || ds[0].Mode != "direct" || reloaded.Settings().Enabled {
		t.Fatalf("failed write changed persisted state: %+v", reloaded.State())
	}
}
