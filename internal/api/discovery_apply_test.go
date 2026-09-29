package api

import (
	"errors"
	"testing"

	"openpass/internal/model"
)

func TestDiscoveredAddressReappliesBindingAndRetriesFailure(t *testing.T) {
	s, rt := selfFixture(t)
	v := s.Store.Settings()
	v.Enabled, v.AutoApply = true, false
	if err := s.Store.UpdateSettings(v); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Store.UpdateDevice("mine", func(d *model.Device) { d.Mode, d.NodeID = "proxy", "available" }); err != nil {
		t.Fatal(err)
	}
	before := s.Store.Devices()
	if err := s.applyDiscoveredAddresses(before); err != nil {
		t.Fatal(err)
	}
	if len(rt.applied.Devices) != 0 {
		t.Fatal("unchanged discovery restarted runtime")
	}
	if _, err := s.Store.UpdateDevice("mine", func(d *model.Device) { d.IP = "192.0.2.99" }); err != nil {
		t.Fatal(err)
	}
	rt.err = errors.New("temporary reload failure")
	if err := s.applyDiscoveredAddresses(before); err == nil {
		t.Fatal("reload failure was hidden")
	}
	if !s.discoveryNeedsApply {
		t.Fatal("failed address update lost retry")
	}
	rt.err = nil
	if err := s.applyDiscoveredAddresses(s.Store.Devices()); err != nil {
		t.Fatal(err)
	}
	if s.discoveryNeedsApply {
		t.Fatal("successful retry remained pending")
	}
	for _, d := range rt.applied.Devices {
		if d.ID == "mine" && (d.IP != "192.0.2.99" || d.Mode != "proxy" || d.NodeID != "available") {
			t.Fatalf("binding did not follow new address: %+v", d)
		}
	}
	rt.applied = model.State{}
	if err := s.applyDiscoveredAddresses(s.Store.Devices()); err != nil {
		t.Fatal(err)
	}
	if len(rt.applied.Devices) != 0 {
		t.Fatal("repeat discovery unnecessarily restarted runtime")
	}
}
