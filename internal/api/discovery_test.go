package api

import (
	"strings"
	"testing"
	"time"
)

func TestLeaseExpiry(t *testing.T) {
	now := time.Unix(1_700_000_000, 0)
	if expiry, ok := parseLeaseExpiry("0"); !ok || !leaseActive(expiry, now) {
		t.Fatal("zero lease should be treated as an active/infinite hint")
	}
	if expiry, ok := parseLeaseExpiry("1699999999"); !ok || leaseActive(expiry, now) {
		t.Fatal("expired lease was treated as active")
	}
	if expiry, ok := parseLeaseExpiry("1700000001"); !ok || !leaseActive(expiry, now) {
		t.Fatal("future lease was treated as expired")
	}
	if _, ok := parseLeaseExpiry("not-a-time"); ok {
		t.Fatal("malformed lease expiry should be retained only as an untrusted hint")
	}
}

func TestNeighborStateFiltersDeadEntries(t *testing.T) {
	for _, tc := range []struct {
		line, state string
		usable      bool
	}{
		{"10.0.0.5 dev br-lan lladdr 02:00:00:00:00:05 REACHABLE", "REACHABLE", true},
		{"10.0.0.6 dev br-lan lladdr 02:00:00:00:00:06 STALE", "STALE", true},
		{"10.0.0.7 dev br-lan lladdr 02:00:00:00:00:07 FAILED", "FAILED", false},
		{"10.0.0.8 dev br-lan INCOMPLETE", "INCOMPLETE", false},
		{"10.0.0.9 lladdr 02:00:00:00:00:09 REACHABLE", "REACHABLE", true},
		{"10.0.0.9 dev br-lan lladdr 02:00:00:00:00:09 PERMANENT", "PERMANENT", false},
	} {
		parts := strings.Fields(tc.line)
		if got := neighborState(parts); got != tc.state {
			t.Errorf("state=%q, want %q", got, tc.state)
		}
		if got := neighborUsable(tc.state); got != tc.usable {
			t.Errorf("usable(%s)=%v, want %v", tc.state, got, tc.usable)
		}
	}
}
