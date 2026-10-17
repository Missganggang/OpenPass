package store

import (
	"os"
	"path/filepath"
	"reflect"
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

func TestL2TPModuleTogglePersistsAndRollsBackOnWriteFailure(t *testing.T) {
	path := filepath.Join(t.TempDir(), "state.json")
	s, err := New(path)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.UpsertNode(model.Node{ID: "vpn", Type: "l2tp", Enabled: true}); err != nil {
		t.Fatal(err)
	}
	if _, err := s.SetL2TPEnabled(false); err != nil || !s.Settings().L2TPDisabled || !s.Nodes()[0].Enabled {
		t.Fatalf("closing module lost saved intent: %v", err)
	}
	before := s.State()
	blocked := filepath.Join(t.TempDir(), "blocked")
	if err := os.WriteFile(blocked, []byte("file"), 0600); err != nil {
		t.Fatal(err)
	}
	s.path = filepath.Join(blocked, "state.json")
	if _, err := s.SetL2TPEnabled(true); err == nil || !reflect.DeepEqual(s.State(), before) {
		t.Fatalf("failed opening changed memory: %v", err)
	}
	s.path = path
	if settings, err := s.SetL2TPEnabled(true); err != nil || settings.L2TPDisabled || !settings.Enabled {
		t.Fatalf("opening did not activate protections: %v", err)
	}
	reloaded, err := New(path)
	if err != nil || reloaded.Settings().L2TPDisabled || !reloaded.Settings().Enabled || !reloaded.Nodes()[0].Enabled {
		t.Fatalf("opening not persisted atomically: %v", err)
	}
}

func TestL2TPImportWriteFailureRollsBackEntireBatch(t *testing.T) {
	s, err := New(filepath.Join(t.TempDir(), "state.json"))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.UpsertNode(model.Node{ID: "existing", Type: "socks5", Enabled: true}); err != nil {
		t.Fatal(err)
	}
	before := s.State()
	blocked := filepath.Join(t.TempDir(), "blocked")
	if err := os.WriteFile(blocked, []byte("file"), 0600); err != nil {
		t.Fatal(err)
	}
	s.path = filepath.Join(blocked, "state.json")
	added, skipped, err := s.ImportL2TP([]model.Node{
		{Address: "first.example.net", Username: "u", Password: "p"},
		{Address: "second.example.net", Username: "u", Password: "p"},
	})
	if err == nil || len(added) != 0 || skipped != 0 || !reflect.DeepEqual(s.State(), before) {
		t.Fatalf("failed batch changed memory: imported=%d skipped=%d err=%v", len(added), skipped, err)
	}
}
