package store

import (
	"errors"
	"os"
	"path/filepath"
	"testing"

	"openpass/internal/model"
	"openpass/internal/nodes"
)

func TestSubscriptionRefreshKeepsNodeIDsAndRemovesUnboundNodes(t *testing.T) {
	s, err := New(filepath.Join(t.TempDir(), "state.json"))
	if err != nil {
		t.Fatal(err)
	}
	sub, got, err := s.UpsertSubscription(model.Subscription{URL: "https://example.test/feed", Name: "Hello"}, []model.Node{
		{URI: "socks5://user:pass@one.example:1080", Type: "socks5", Address: "one.example", Port: 1080},
		{URI: "socks5://user:pass@two.example:1080", Type: "socks5", Address: "two.example", Port: 1080},
	})
	if err != nil || len(got) != 2 {
		t.Fatalf("initial import: %v %#v", err, got)
	}
	firstID := got[0].ID
	if got[0].Subscription != sub.ID || sub.Name != "Hello" {
		t.Fatalf("subscription metadata not attached: %#v %#v", sub, got[0])
	}
	_, refreshed, err := s.UpsertSubscription(sub, []model.Node{
		{URI: "socks5://user:pass@one.example:1080", Type: "socks5", Address: "one.example", Port: 1080, Name: "One"},
	})
	if err != nil || len(refreshed) != 1 || refreshed[0].ID != firstID {
		t.Fatalf("refresh should retain existing ID: %v %#v", err, refreshed)
	}
	if len(s.Nodes()) != 1 || len(s.Subscriptions()) != 1 {
		t.Fatalf("unexpected state after refresh: nodes=%d subs=%d", len(s.Nodes()), len(s.Subscriptions()))
	}
}

func TestSubscriptionRemovalAndRefreshRejectBoundDevices(t *testing.T) {
	s, err := New(filepath.Join(t.TempDir(), "state.json"))
	if err != nil {
		t.Fatal(err)
	}
	sub, ns, err := s.UpsertSubscription(model.Subscription{URL: "https://example.test/feed"}, []model.Node{{URI: "socks5://one.example:1080", Type: "socks5", Address: "one.example", Port: 1080}})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.UpsertDevice(model.Device{ID: "d1", IP: "192.0.2.5", MAC: "AA:BB:CC:DD:EE:FF", Mode: "proxy", NodeID: ns[0].ID}); err != nil {
		t.Fatal(err)
	}
	_, _, err = s.UpsertSubscription(sub, nil)
	var inUse *SubscriptionInUseError
	if !errors.As(err, &inUse) || len(inUse.Devices) != 1 {
		t.Fatalf("refresh should report bound device, got %v", err)
	}
	if err := s.DeleteSubscription(sub.ID); !errors.As(err, &inUse) {
		t.Fatalf("delete should report bound device, got %v", err)
	}
	if len(s.Nodes()) != 1 || len(s.Subscriptions()) != 1 {
		t.Fatal("bound subscription was modified")
	}
	if _, err := s.UpdateDevice("d1", func(d *model.Device) { d.Mode = "direct" }); err != nil {
		t.Fatal(err)
	}
	if err := s.DeleteSubscription(sub.ID); err != nil {
		t.Fatal(err)
	}
	if len(s.Nodes()) != 0 || len(s.Subscriptions()) != 0 {
		t.Fatal("subscription nodes were not removed")
	}
}

func TestStateLoadsSubscriptionsAsEmptyListForLegacyState(t *testing.T) {
	path := filepath.Join(t.TempDir(), "state.json")
	if err := os.WriteFile(path, []byte(`{"nodes":[{"id":"n","subscription":"legacy-sub"}],"devices":[],"settings":{},"dns":[]}`), 0600); err != nil {
		t.Fatal(err)
	}
	s, err := New(path)
	if err != nil {
		t.Fatal(err)
	}
	if s.Subscriptions() == nil {
		t.Fatal("legacy state should expose an empty subscription list")
	}
	if got := s.Nodes(); len(got) != 1 || got[0].Subscription != "legacy-sub" {
		t.Fatalf("legacy node subscription was not migrated: %#v", got)
	}
}

func TestSubscriptionRefreshKeepsDistinctIDsForDuplicateURIs(t *testing.T) {
	s, err := New(filepath.Join(t.TempDir(), "state.json"))
	if err != nil {
		t.Fatal(err)
	}
	n, err := nodes.Parse("socks5://user:pass@feed.example:1080#Duplicate")
	if err != nil {
		t.Fatal(err)
	}
	sub, original, err := s.UpsertSubscription(model.Subscription{Name: "Hello"}, []model.Node{n, n})
	if err != nil {
		t.Fatal(err)
	}
	if len(original) != 2 || original[0].ID == original[1].ID {
		t.Fatalf("initial duplicate entries must have distinct IDs: %#v", original)
	}
	for i, old := range original {
		if _, err := s.UpsertDevice(model.Device{ID: old.ID + "-device", MAC: old.ID, Mode: "proxy", NodeID: old.ID, Hidden: i == 1}); err != nil {
			t.Fatal(err)
		}
	}
	for i := 0; i < 2; i++ {
		_, refreshed, err := s.UpsertSubscription(sub, []model.Node{n, n})
		if err != nil {
			t.Fatal(err)
		}
		if len(refreshed) != 2 || refreshed[0].ID != original[0].ID || refreshed[1].ID != original[1].ID {
			t.Fatalf("refresh must preserve each duplicate entry and binding: %#v", refreshed)
		}
	}
}

func TestSubscriptionRefreshIgnoresDisplayNamesButKeepsTransportIdentity(t *testing.T) {
	s, err := New(filepath.Join(t.TempDir(), "state.json"))
	if err != nil {
		t.Fatal(err)
	}
	first := model.Node{Type: "vless", Address: "feed.example", Port: 443, UUID: "user", Name: "Old name", URI: "vless://user@feed.example:443?type=ws&path=%2Fa#Old%20name", Network: "ws", Path: "/a", Remark: "Local remark"}
	second := first
	second.URI, second.Path = "vless://user@feed.example:443?type=ws&path=%2Fb#Second", "/b"
	sub, original, err := s.UpsertSubscription(model.Subscription{Name: "Hello"}, []model.Node{first, second})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.UpsertDevice(model.Device{ID: "offline-hidden", MAC: "AA:BB:CC:DD:EE:01", Mode: "proxy", NodeID: original[0].ID, Hidden: true}); err != nil {
		t.Fatal(err)
	}
	first.Name, first.URI, first.Remark = "New name", "vless://user@feed.example:443?path=%2Fa&type=ws#New%20name", ""
	_, refreshed, err := s.UpsertSubscription(sub, []model.Node{second, first})
	if err != nil {
		t.Fatal(err)
	}
	if len(refreshed) != 2 || refreshed[0].ID != original[1].ID || refreshed[1].ID != original[0].ID {
		t.Fatalf("reordered transports must retain IDs despite display-name changes: %#v", refreshed)
	}
	if refreshed[1].Name != "New name" || refreshed[1].Remark != "Local remark" {
		t.Fatalf("upstream name and local remark should both survive: %#v", refreshed[1])
	}
	first.Path = "/changed"
	if _, _, err := s.UpsertSubscription(sub, []model.Node{second, first}); err == nil {
		t.Fatal("changing a bound node's transport must not silently repoint its binding")
	}
}

func TestSubscriptionCannotReuseForeignNodeIDs(t *testing.T) {
	s, err := New(filepath.Join(t.TempDir(), "state.json"))
	if err != nil {
		t.Fatal(err)
	}
	manual, err := s.UpsertNode(model.Node{ID: "manual", Type: "socks5", Address: "manual.example", Port: 1080, Enabled: true})
	if err != nil {
		t.Fatal(err)
	}
	incoming := model.Node{ID: manual.ID, Type: "socks5", Address: "feed.example", Port: 1080}
	sub, first, err := s.UpsertSubscription(model.Subscription{Name: "First"}, []model.Node{incoming, incoming})
	if err != nil {
		t.Fatal(err)
	}
	if len(first) != 2 || first[0].ID == manual.ID || first[1].ID == manual.ID || first[0].ID == first[1].ID {
		t.Fatalf("feed input must not choose local IDs: %#v", first)
	}
	incoming.ID = first[0].ID
	_, second, err := s.UpsertSubscription(model.Subscription{Name: "Second"}, []model.Node{incoming})
	if err != nil {
		t.Fatal(err)
	}
	if second[0].ID == first[0].ID {
		t.Fatal("a different subscription reused an existing node ID")
	}
	_, refreshed, err := s.UpsertSubscription(sub, []model.Node{incoming, incoming})
	if err != nil || refreshed[0].ID != first[0].ID || refreshed[1].ID != first[1].ID {
		t.Fatalf("local matching should preserve only this subscription's IDs: %v %#v", err, refreshed)
	}
	if got := s.Nodes(); len(got) != 4 || got[0].ID != manual.ID || got[0].Address != manual.Address {
		t.Fatalf("subscription changes modified a manual node: %#v", got)
	}
}
