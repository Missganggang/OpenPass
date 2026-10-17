package store

import (
	"strings"
	"time"

	"openpass/internal/model"
)

// SetL2TPEnabled persists the module switch and any necessary global activation
// together. Individual connection intentions and device bindings are retained.
func (s *Store) SetL2TPEnabled(enabled bool) (model.Settings, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	previous := s.data.Settings
	s.data.Settings.L2TPDisabled = !enabled
	if enabled {
		for _, n := range s.data.Nodes {
			if n.Type == "l2tp" && n.Enabled {
				s.data.Settings.Enabled = true
				break
			}
		}
	}
	if err := s.saveLocked(); err != nil {
		s.data.Settings = previous
		return previous, err
	}
	return s.data.Settings, nil
}

// ImportL2TP atomically appends validated connections. Identical credentials
// are skipped both within a batch and against saved connections, with no
// delimiter concatenation that could collide with colons in passwords.
func (s *Store) ImportL2TP(incoming []model.Node) ([]model.Node, int, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	type credentialKey struct{ server, username, password string }
	key := func(n model.Node) credentialKey {
		return credentialKey{strings.ToLower(n.Address), n.Username, n.Password}
	}
	seen := make(map[credentialKey]bool)
	for _, n := range s.data.Nodes {
		if n.Type == "l2tp" {
			seen[key(n)] = true
		}
	}
	added := make([]model.Node, 0, len(incoming))
	skipped := 0
	now := time.Now()
	for _, n := range incoming {
		k := key(n)
		if seen[k] {
			skipped++
			continue
		}
		seen[k] = true
		n.ID, n.Type, n.Subscription, n.URI = ID("node"), "l2tp", "", ""
		n.Enabled = false
		n.CreatedAt, n.UpdatedAt = now, now
		added = append(added, n)
	}
	if len(added) == 0 {
		return added, skipped, nil
	}
	previous := s.data.Nodes
	nodes := make([]model.Node, 0, len(previous)+len(added))
	nodes = append(nodes, previous...)
	s.data.Nodes = append(nodes, added...)
	if err := s.saveLocked(); err != nil {
		s.data.Nodes = previous
		return nil, 0, err
	}
	return added, skipped, nil
}
