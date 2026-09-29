package store

import (
	"errors"
	"os"
	"path/filepath"
	"testing"

	"openpass/internal/model"
)

func TestDeleteNodeChecksReferencesAndRollsBackOnSaveFailure(t *testing.T) {
	path := filepath.Join(t.TempDir(), "state.json")
	s, err := New(path)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.UpsertNode(model.Node{ID: "node", Password: "preserved"}); err != nil {
		t.Fatal(err)
	}
	if _, err := s.UpsertDevice(model.Device{ID: "device", Mode: "proxy", NodeID: "node", Hidden: true}); err != nil {
		t.Fatal(err)
	}
	var inUse *NodeInUseError
	if !errors.As(s.DeleteNode("node"), &inUse) || len(inUse.Devices) != 1 {
		t.Fatal("store allowed deletion of a referenced node")
	}
	if _, err := s.UpdateDevice("device", func(d *model.Device) { d.Mode = "direct" }); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(path+".tmp", 0700); err != nil {
		t.Fatal(err)
	}
	if err := s.DeleteNode("node"); err == nil {
		t.Fatal("expected disk failure")
	}
	if nodes := s.Nodes(); len(nodes) != 1 || nodes[0].Password != "preserved" {
		t.Fatal("failed deletion changed in-memory node")
	}
	reloaded, err := New(path)
	if err != nil {
		t.Fatal(err)
	}
	if len(reloaded.Nodes()) != 1 {
		t.Fatal("failed deletion changed persisted nodes")
	}
}
