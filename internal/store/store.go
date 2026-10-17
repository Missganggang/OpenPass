package store

import (
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"openpass/internal/model"
)

type Store struct {
	mu   sync.RWMutex
	path string
	data model.State
}

func New(path string) (*Store, error) {
	s := &Store{path: path}
	if err := s.load(); err != nil {
		return nil, err
	}
	return s, nil
}

func (s *Store) load() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.data = model.State{Settings: model.DefaultSettings(), DNS: model.DefaultDNS(), Devices: []model.Device{}, Nodes: []model.Node{}, Subscriptions: []model.Subscription{}}
	b, err := os.ReadFile(s.path)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return err
	}
	if err := json.Unmarshal(b, &s.data); err != nil {
		return err
	}
	defaults := model.DefaultSettings()
	if s.data.Settings.DefaultDNS == "" {
		s.data.Settings.DefaultDNS = defaults.DefaultDNS
	}
	if s.data.Settings.DefaultMode == "" {
		s.data.Settings.DefaultMode = defaults.DefaultMode
	}
	if s.data.Settings.URLTestAddress == "" {
		s.data.Settings.URLTestAddress = defaults.URLTestAddress
	}
	if s.data.Settings.URLTestRegion == "" {
		s.data.Settings.URLTestRegion = defaults.URLTestRegion
	}
	if s.data.Settings.WebPort == 0 {
		s.data.Settings.WebPort = defaults.WebPort
	}
	s.data.DNS = mergeDNSProfiles(s.data.DNS)
	if s.data.Devices == nil {
		s.data.Devices = []model.Device{}
	}
	if s.data.Nodes == nil {
		s.data.Nodes = []model.Node{}
	}
	if s.data.Subscriptions == nil {
		s.data.Subscriptions = []model.Subscription{}
	}
	return nil
}

// mergeDNSProfiles adds new built-in profiles to upgraded installations while
// retaining their selected IDs, custom endpoints and user-defined profiles.
// Only built-in display names are refreshed so the pinned resolver IP is clear.
func mergeDNSProfiles(existing []model.DNS) []model.DNS {
	out := append([]model.DNS(nil), existing...)
	for _, preset := range model.DefaultDNS() {
		found := false
		for i := range out {
			if out[i].ID == preset.ID {
				out[i].Name = preset.Name
				found = true
			}
		}
		if !found {
			out = append(out, preset)
		}
	}
	return out
}

func (s *Store) saveLocked() error {
	if err := os.MkdirAll(filepath.Dir(s.path), 0755); err != nil {
		return err
	}
	b, err := json.MarshalIndent(s.data, "", "  ")
	if err != nil {
		return err
	}
	tmp := s.path + ".tmp"
	if err = os.WriteFile(tmp, b, 0600); err != nil {
		return err
	}
	return os.Rename(tmp, s.path)
}

func (s *Store) State() model.State {
	s.mu.RLock()
	defer s.mu.RUnlock()
	out := s.data
	out.Devices = append([]model.Device(nil), s.data.Devices...)
	out.Nodes = append([]model.Node(nil), s.data.Nodes...)
	out.Subscriptions = append([]model.Subscription(nil), s.data.Subscriptions...)
	out.DNS = append([]model.DNS(nil), s.data.DNS...)
	return out
}
func (s *Store) Devices() []model.Device {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return append([]model.Device(nil), s.data.Devices...)
}
func (s *Store) Nodes() []model.Node {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return append([]model.Node{}, s.data.Nodes...)
}
func (s *Store) Subscriptions() []model.Subscription {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return append([]model.Subscription{}, s.data.Subscriptions...)
}
func (s *Store) Settings() model.Settings { s.mu.RLock(); defer s.mu.RUnlock(); return s.data.Settings }
func (s *Store) DNS() []model.DNS {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return append([]model.DNS(nil), s.data.DNS...)
}

func ID(prefix string) string {
	var b [6]byte
	_, _ = rand.Read(b[:])
	return prefix + "_" + hex.EncodeToString(b[:])
}

func (s *Store) UpsertDevice(d model.Device) (model.Device, error) {
	return s.UpsertDevicePolicy(d, false)
}

// UpsertDevicePolicy saves a device and any required global activation in a
// single state-file write. A failed write restores the in-memory state too.
func (s *Store) UpsertDevicePolicy(d model.Device, activateProxy bool) (saved model.Device, err error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	previous := s.data
	previous.Devices = append([]model.Device(nil), s.data.Devices...)
	defer func() {
		if err != nil {
			s.data = previous
		}
	}()
	now := time.Now()
	if d.ID == "" {
		d.ID = ID("dev")
	}
	if d.Mode == "block" {
		d.Mode = "blocked"
	}
	if d.Mode == "" {
		d.Mode = s.data.Settings.DefaultMode
	}
	if d.Mode == "block" {
		d.Mode = "blocked"
	}
	if d.Mode != "proxy" {
		d.NodeID = ""
	}
	if activateProxy && d.Mode == "proxy" {
		s.data.Settings.Enabled = true
	}
	for i := range s.data.Devices {
		sameMAC := d.MAC != "" && normalizeMAC(s.data.Devices[i].MAC) == normalizeMAC(d.MAC)
		if s.data.Devices[i].ID == d.ID || sameMAC {
			if sameMAC {
				d.ID = s.data.Devices[i].ID
			}
			if d.FirstSeen.IsZero() {
				d.FirstSeen = s.data.Devices[i].FirstSeen
			}
			s.data.Devices[i] = d
			return d, s.saveLocked()
		}
	}
	if d.FirstSeen.IsZero() {
		d.FirstSeen = now
	}
	d.LastSeen = now
	s.data.Devices = append(s.data.Devices, d)
	return d, s.saveLocked()
}

func normalizeMAC(v string) string {
	return strings.ToUpper(strings.ReplaceAll(strings.TrimSpace(v), "-", ":"))
}
func (s *Store) UpdateDevice(id string, fn func(*model.Device)) (model.Device, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	for i := range s.data.Devices {
		if s.data.Devices[i].ID == id {
			fn(&s.data.Devices[i])
			if s.data.Devices[i].Mode != "proxy" {
				s.data.Devices[i].NodeID = ""
			}
			return s.data.Devices[i], s.saveLocked()
		}
	}
	return model.Device{}, os.ErrNotExist
}
func (s *Store) DeleteDevice(id string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	for i, d := range s.data.Devices {
		if d.ID == id {
			previous := append([]model.Device(nil), s.data.Devices...)
			s.data.Devices = append(s.data.Devices[:i], s.data.Devices[i+1:]...)
			if err := s.saveLocked(); err != nil {
				s.data.Devices = previous
				return err
			}
			return nil
		}
	}
	return os.ErrNotExist
}
func (s *Store) UpsertNode(n model.Node) (model.Node, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	now := time.Now()
	if n.ID == "" {
		n.ID = ID("node")
	}
	if n.CreatedAt.IsZero() {
		n.CreatedAt = now
	}
	n.UpdatedAt = now
	for i := range s.data.Nodes {
		if s.data.Nodes[i].ID == n.ID {
			n.CreatedAt = s.data.Nodes[i].CreatedAt
			s.data.Nodes[i] = n
			return n, s.saveLocked()
		}
	}
	s.data.Nodes = append(s.data.Nodes, n)
	return n, s.saveLocked()
}

// NodeInUseError includes every referring device, including offline/hidden
// clients. The reference check and deletion share the store's write lock.
type NodeInUseError struct{ Devices []model.Device }

func (e *NodeInUseError) Error() string {
	return fmt.Sprintf("该节点仍被 %d 台设备绑定，请先解绑设备后再删除", len(e.Devices))
}

func (s *Store) DeleteNode(id string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	for i, n := range s.data.Nodes {
		if n.ID == id {
			var bound []model.Device
			for _, device := range s.data.Devices {
				if device.NodeID == id {
					bound = append(bound, device)
				}
			}
			if len(bound) > 0 {
				return &NodeInUseError{Devices: bound}
			}
			previous := append([]model.Node{}, s.data.Nodes...)
			s.data.Nodes = append(s.data.Nodes[:i], s.data.Nodes[i+1:]...)
			if err := s.saveLocked(); err != nil {
				s.data.Nodes = previous
				return err
			}
			return nil
		}
	}
	return os.ErrNotExist
}

// UpdateNode applies a small in-place update while preserving credentials and
// transport fields that are intentionally omitted by PATCH requests.
func (s *Store) UpdateNode(id string, fn func(*model.Node) error) (model.Node, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	for i := range s.data.Nodes {
		if s.data.Nodes[i].ID != id {
			continue
		}
		candidate := s.data.Nodes[i]
		if err := fn(&candidate); err != nil {
			return model.Node{}, err
		}
		candidate.UpdatedAt = time.Now()
		s.data.Nodes[i] = candidate
		if err := s.saveLocked(); err != nil {
			return model.Node{}, err
		}
		return s.data.Nodes[i], nil
	}
	return model.Node{}, os.ErrNotExist
}

// SubscriptionInUseError prevents a subscription refresh/removal from
// silently orphaning devices whose proxy policy points at one of its nodes.
// It intentionally embeds the concrete devices so the API can show the user
// exactly which clients must be unbound first.
type SubscriptionInUseError struct{ Devices []model.Device }

func (e *SubscriptionInUseError) Error() string {
	return fmt.Sprintf("该订阅仍有 %d 台设备绑定节点，请先解绑设备后再操作", len(e.Devices))
}

// UpsertSubscription stores a subscription and atomically replaces its
// imported nodes. Existing node IDs are retained when their connection fields
// are unchanged so device bindings survive a refresh. Nodes removed upstream
// are rejected while still referenced by a device.
func (s *Store) UpsertSubscription(sub model.Subscription, incoming []model.Node) (model.Subscription, []model.Node, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	previous := s.data
	previous.Devices = append([]model.Device(nil), s.data.Devices...)
	previous.Nodes = append([]model.Node(nil), s.data.Nodes...)
	previous.Subscriptions = append([]model.Subscription(nil), s.data.Subscriptions...)

	now := time.Now()
	if sub.ID == "" {
		sub.ID = ID("sub")
	}
	if sub.Name == "" {
		sub.Name = sub.URL
	}
	if sub.CreatedAt.IsZero() {
		sub.CreatedAt = now
	}
	for i := range s.data.Subscriptions {
		if s.data.Subscriptions[i].ID == sub.ID {
			sub.CreatedAt = s.data.Subscriptions[i].CreatedAt
			if sub.URL == "" {
				sub.URL = s.data.Subscriptions[i].URL
			}
			if sub.Name == "" {
				sub.Name = s.data.Subscriptions[i].Name
			}
			break
		}
	}
	sub.Enabled = true
	sub.LastSyncAt = now
	sub.LastError = ""
	sub.UpdatedAt = now

	// Display names and URI encoding can change without changing a connection.
	// Keep a queue for each connection so duplicate feed entries each retain
	// their own ID instead of reusing one outbound tag more than once.
	existing := make(map[string][]model.Node)
	for _, n := range s.data.Nodes {
		if n.Subscription == sub.ID {
			key := nodeRefreshKey(n)
			existing[key] = append(existing[key], n)
		}
	}
	newNodes := make([]model.Node, 0, len(incoming))
	seen := make(map[string]bool)
	for _, n := range incoming {
		// Remote input must not select IDs belonging to manual nodes or other
		// subscriptions. Only a match in this subscription may retain an ID.
		n.ID = ""
		n.Subscription = sub.ID
		n.Enabled = true
		key := nodeRefreshKey(n)
		if matches := existing[key]; len(matches) > 0 {
			old := matches[0]
			existing[key] = matches[1:]
			n.ID = old.ID
			n.CreatedAt = old.CreatedAt
			if n.Remark == "" {
				n.Remark = old.Remark
			}
			seen[old.ID] = true
		}
		if n.ID == "" {
			n.ID = ID("node")
		}
		if n.CreatedAt.IsZero() {
			n.CreatedAt = now
		}
		n.UpdatedAt = now
		newNodes = append(newNodes, n)
	}
	var removedIDs []string
	for _, old := range s.data.Nodes {
		if old.Subscription == sub.ID && !seen[old.ID] {
			removedIDs = append(removedIDs, old.ID)
		}
	}
	if len(removedIDs) > 0 {
		var bound []model.Device
		for _, d := range s.data.Devices {
			for _, id := range removedIDs {
				if d.NodeID == id {
					bound = append(bound, d)
					break
				}
			}
		}
		if len(bound) > 0 {
			return model.Subscription{}, nil, &SubscriptionInUseError{Devices: bound}
		}
	}
	kept := make([]model.Node, 0, len(s.data.Nodes)+len(newNodes))
	for _, old := range s.data.Nodes {
		if old.Subscription != sub.ID {
			kept = append(kept, old)
		}
	}
	kept = append(kept, newNodes...)
	s.data.Nodes = kept
	found := false
	for i := range s.data.Subscriptions {
		if s.data.Subscriptions[i].ID == sub.ID {
			s.data.Subscriptions[i] = sub
			found = true
			break
		}
	}
	if !found {
		s.data.Subscriptions = append(s.data.Subscriptions, sub)
	}
	if err := s.saveLocked(); err != nil {
		s.data = previous
		return model.Subscription{}, nil, err
	}
	return sub, append([]model.Node(nil), newNodes...), nil
}

// UpdateSubscription edits display metadata without touching its imported
// nodes. This is used by the UI's rename/remark controls.
func (s *Store) UpdateSubscription(id string, fn func(*model.Subscription) error) (model.Subscription, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	for i := range s.data.Subscriptions {
		if s.data.Subscriptions[i].ID != id {
			continue
		}
		candidate := s.data.Subscriptions[i]
		if err := fn(&candidate); err != nil {
			return model.Subscription{}, err
		}
		candidate.UpdatedAt = time.Now()
		previous := s.data.Subscriptions[i]
		s.data.Subscriptions[i] = candidate
		if err := s.saveLocked(); err != nil {
			s.data.Subscriptions[i] = previous
			return model.Subscription{}, err
		}
		return candidate, nil
	}
	return model.Subscription{}, os.ErrNotExist
}

func nodeRefreshKey(n model.Node) string {
	// Include every supported connection field, but omit display metadata,
	// the original URI (whose fragment / vmess ps contains the name), and
	// local IDs, timestamps and test results. JSON avoids delimiter collisions
	// in credentials and remains stable for this fixed struct.
	connection := model.Node{
		Type: n.Type, Address: n.Address, Port: n.Port,
		UUID: n.UUID, Password: n.Password, Method: n.Method,
		Flow: n.Flow, Network: n.Network, SNI: n.SNI,
		RealityPublicKey: n.RealityPublicKey, RealityShortID: n.RealityShortID,
		Fingerprint: n.Fingerprint, Host: n.Host, Path: n.Path, TLS: n.TLS,
	}
	b, _ := json.Marshal(connection)
	return string(b)
}

// DeleteSubscription removes the subscription and all of its nodes after
// checking every device (including offline and hidden clients) for a binding.
func (s *Store) DeleteSubscription(id string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	idx := -1
	for i, sub := range s.data.Subscriptions {
		if sub.ID == id {
			idx = i
			break
		}
	}
	if idx < 0 {
		return os.ErrNotExist
	}
	var nodeIDs []string
	for _, n := range s.data.Nodes {
		if n.Subscription == id {
			nodeIDs = append(nodeIDs, n.ID)
		}
	}
	if len(nodeIDs) > 0 {
		var bound []model.Device
		for _, d := range s.data.Devices {
			for _, nid := range nodeIDs {
				if d.NodeID == nid {
					bound = append(bound, d)
					break
				}
			}
		}
		if len(bound) > 0 {
			return &SubscriptionInUseError{Devices: bound}
		}
	}
	previous := s.data
	previous.Nodes = append([]model.Node(nil), s.data.Nodes...)
	previous.Subscriptions = append([]model.Subscription(nil), s.data.Subscriptions...)
	filtered := s.data.Nodes[:0]
	for _, n := range s.data.Nodes {
		if n.Subscription != id {
			filtered = append(filtered, n)
		}
	}
	s.data.Nodes = filtered
	s.data.Subscriptions = append(s.data.Subscriptions[:idx], s.data.Subscriptions[idx+1:]...)
	if err := s.saveLocked(); err != nil {
		s.data = previous
		return err
	}
	return nil
}

func (s *Store) UpdateSettings(v model.Settings) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.data.Settings = v
	return s.saveLocked()
}
