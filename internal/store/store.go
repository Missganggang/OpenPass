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
	s.data = model.State{Settings: model.DefaultSettings(), DNS: model.DefaultDNS(), Devices: []model.Device{}, Nodes: []model.Node{}}
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

func (s *Store) UpdateSettings(v model.Settings) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.data.Settings = v
	return s.saveLocked()
}
