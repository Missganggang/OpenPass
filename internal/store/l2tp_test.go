package store

import (
	"os"
	"path/filepath"
	"testing"

	"openpass/internal/model"
)

func TestL2TPActivationPersistsAtomically(t *testing.T) {
	path := filepath.Join(t.TempDir(), "state.json")
	s, err := New(path)
	if err != nil {
		t.Fatal(err)
	}
	n, err := s.UpsertNodePolicy(model.Node{Type: "l2tp", Name: "Test", Username: "user", Password: "pass", MTU: 1400, Enabled: true}, true)
	if err != nil {
		t.Fatal(err)
	}
	reloaded, err := New(path)
	if err != nil || !reloaded.Settings().Enabled || len(reloaded.Nodes()) != 1 {
		t.Fatalf("state did not round-trip: err=%v", err)
	}
	saved := reloaded.Nodes()[0]
	if saved.ID != n.ID || saved.Type != "l2tp" || saved.Username != "user" || saved.Password != "pass" || saved.MTU != 1400 || !saved.Enabled {
		t.Fatalf("connection fields lost on reload: %+v", saved)
	}
}

func TestNodePolicyWriteFailureRollsBackNodeAndGlobalState(t *testing.T) {
	parent := filepath.Join(t.TempDir(), "blocked")
	s, err := New(filepath.Join(parent, "state.json"))
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(parent, []byte("not a directory"), 0600); err != nil {
		t.Fatal(err)
	}
	_, err = s.UpsertNodePolicy(model.Node{Type: "l2tp", Enabled: true}, true)
	if err == nil || s.Settings().Enabled || len(s.Nodes()) != 0 {
		t.Fatalf("failed activation changed state: %v %+v", err, s.State())
	}
}
