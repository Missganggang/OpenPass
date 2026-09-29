package store

import (
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
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
		s.data.Settings = defaults
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
	if len(s.data.DNS) == 0 {
		s.data.DNS = model.DefaultDNS()
	}
	if s.data.Devices == nil {
		s.data.Devices = []model.Device{}
	}
	if s.data.Nodes == nil {
		s.data.Nodes = []model.Node{}
	}
	return nil
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
	return append([]model.Node(nil), s.data.Nodes...)
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
	s.mu.Lock()
	defer s.mu.Unlock()
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
			s.data.Devices = append(s.data.Devices[:i], s.data.Devices[i+1:]...)
			return s.saveLocked()
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
func (s *Store) DeleteNode(id string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	for i, n := range s.data.Nodes {
		if n.ID == id {
			s.data.Nodes = append(s.data.Nodes[:i], s.data.Nodes[i+1:]...)
			return s.saveLocked()
		}
	}
	return os.ErrNotExist
}
func (s *Store) UpdateSettings(v model.Settings) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.data.Settings = v
	return s.saveLocked()
}
