package runtime

import (
	"context"
	"crypto/sha256"
	"encoding/binary"
	"encoding/json"
	"fmt"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"sort"
	"strconv"
	"strings"
	"time"

	"openpass/internal/l2tp"
	"openpass/internal/model"
)

const l2tpRouteProtocol = "242"
const l2tpRulePriority = "8000" // Before sing-box auto-route (9000).

type l2tpRoute struct {
	ID      string   `json:"id"`
	Device  string   `json:"device"`
	Sources []string `json:"sources"`
	Up      bool     `json:"up"`
}

type l2tpRoutes struct {
	path    string
	run     func(...string) ([]byte, error)
	current []l2tpRoute
	loaded  bool
	dirty   bool
}

func newL2TPRoutes() *l2tpRoutes {
	return &l2tpRoutes{path: "/var/run/openpass/l2tp-routes.json", run: func(args ...string) ([]byte, error) {
		ctx, cancel := context.WithTimeout(context.Background(), 4*time.Second)
		defer cancel()
		return exec.CommandContext(ctx, "ip", args...).CombinedOutput()
	}}
}

func l2tpTable(id string) string {
	h := sha256.Sum256([]byte(id))
	return strconv.FormatUint(uint64(1200000000+binary.BigEndian.Uint32(h[:4])%1000000000), 10)
}

func l2tpRoutePlan(st model.State, statuses map[string]l2tp.Status) []l2tpRoute {
	if !st.Settings.Enabled {
		return nil
	}
	var routes []l2tpRoute
	for _, n := range st.Nodes {
		if n.Type != "l2tp" {
			continue
		}
		r := l2tpRoute{ID: n.ID, Device: l2tp.DeviceName(n.ID)}
		for _, d := range st.Devices {
			if d.Mode != "proxy" || d.NodeID != n.ID {
				continue
			}
			if ip := net.ParseIP(d.IP); ip != nil && ip.To4() != nil {
				r.Sources = append(r.Sources, ip.String()+"/32")
			}
		}
		status := statuses[n.ID]
		r.Up = n.Enabled && status.Connected && status.Device == r.Device
		if n.Enabled || len(r.Sources) > 0 {
			sort.Strings(r.Sources)
			routes = append(routes, r)
		}
	}
	sort.Slice(routes, func(i, j int) bool { return routes[i].ID < routes[j].ID })
	return routes
}

func (r *l2tpRoutes) command(args ...string) error {
	output, err := r.run(args...)
	if err != nil {
		return fmt.Errorf("L2TP 路由操作失败：%s: %w", strings.TrimSpace(string(output)), err)
	}
	return nil
}

func missingRoute(output []byte) bool {
	s := strings.ToLower(string(output))
	return strings.Contains(s, "no such file") || strings.Contains(s, "no such process") || strings.Contains(s, "does not exist") || strings.Contains(s, "cannot find device")
}

func (r *l2tpRoutes) remove(args ...string) error {
	output, err := r.run(args...)
	if err != nil && !missingRoute(output) {
		return fmt.Errorf("清理 L2TP 路由失败：%s: %w", strings.TrimSpace(string(output)), err)
	}
	return nil
}

func (r *l2tpRoutes) load() error {
	if r.loaded {
		return nil
	}
	b, err := os.ReadFile(r.path)
	if os.IsNotExist(err) {
		r.loaded = true
		return nil
	}
	if err != nil {
		return err
	}
	if err = json.Unmarshal(b, &r.current); err != nil {
		return fmt.Errorf("读取 L2TP 路由记录失败：%w", err)
	}
	for _, route := range r.current {
		if route.ID == "" || route.Device != l2tp.DeviceName(route.ID) {
			return fmt.Errorf("L2TP 路由记录无效")
		}
		for _, source := range route.Sources {
			ip, network, err := net.ParseCIDR(source)
			if err != nil || ip.To4() == nil || network.String() != ip.String()+"/32" {
				return fmt.Errorf("L2TP 来源记录无效")
			}
		}
	}
	r.loaded = true
	r.dirty = true // A previous process may have stopped midway through apply.
	return nil
}

func (r *l2tpRoutes) save(routes []l2tpRoute) error {
	b, err := json.Marshal(routes)
	if err != nil {
		return err
	}
	if err = os.MkdirAll(filepath.Dir(r.path), 0700); err != nil {
		return err
	}
	if err = os.WriteFile(r.path+".tmp", b, 0600); err != nil {
		return err
	}
	return os.Rename(r.path+".tmp", r.path)
}

func (r *l2tpRoutes) deleteRules(route l2tpRoute) error {
	table := l2tpTable(route.ID)
	for _, source := range route.Sources {
		if err := r.remove("-4", "rule", "del", "pref", l2tpRulePriority, "from", source, "table", table, "protocol", l2tpRouteProtocol); err != nil {
			return err
		}
	}
	if route.Up {
		return r.remove("-4", "rule", "del", "pref", l2tpRulePriority, "oif", route.Device, "table", table, "protocol", l2tpRouteProtocol)
	}
	return nil
}

func (r *l2tpRoutes) sync(plan []l2tpRoute) (result error) {
	defer func() {
		if result != nil {
			r.dirty = true
		}
	}()
	if err := r.load(); err != nil {
		return err
	}
	if len(plan) == 0 && len(r.current) == 0 {
		r.dirty = false
		return nil
	}
	known := map[string]l2tpRoute{}
	tables := map[string]string{}
	for _, old := range r.current {
		known[old.ID] = old
	}
	for _, next := range plan {
		table := l2tpTable(next.ID)
		if id, exists := tables[table]; exists && id != next.ID {
			return fmt.Errorf("L2TP 路由表冲突，请重新添加其中一个连接")
		}
		tables[table] = next.ID
		if _, owned := known[next.ID]; !owned {
			// Never adopt a table already populated by another application.
			output, err := r.run("-j", "-4", "route", "show", "table", table)
			if err != nil && !missingRoute(output) {
				return fmt.Errorf("无法检查 L2TP 路由表，请安装 ip-full：%s", strings.TrimSpace(string(output)))
			}
			if err == nil {
				var routes []json.RawMessage
				if json.Unmarshal(output, &routes) != nil {
					return fmt.Errorf("L2TP 需要支持 JSON 的 ip-full")
				}
				if len(routes) > 0 {
					return fmt.Errorf("L2TP 路由表已被其他程序占用")
				}
			}
		}
	}
	changed := r.dirty || !reflect.DeepEqual(plan, r.current)
	if changed {
		// Keep a journal before the first mutation, so cleanup after a daemon
		// crash also knows about partially-created route tables and rules.
		journal := append([]l2tpRoute(nil), r.current...)
		for _, next := range plan {
			found := false
			for i := range journal {
				if journal[i].ID != next.ID {
					continue
				}
				found = true
				journal[i].Up = journal[i].Up || next.Up
				for _, source := range next.Sources {
					seen := false
					for _, oldSource := range journal[i].Sources {
						if source == oldSource {
							seen = true
						}
					}
					if !seen {
						journal[i].Sources = append(journal[i].Sources, source)
					}
				}
			}
			if !found {
				journal = append(journal, next)
			}
		}
		if err := r.save(journal); err != nil {
			return err
		}
		r.current = journal
		r.dirty = true
	}
	for _, next := range plan {
		table := l2tpTable(next.ID)
		// Install a terminal route first. A removed PPP default must never
		// continue lookup in the router's WAN/main routing table.
		if err := r.command("-4", "route", "replace", "unreachable", "default", "metric", "42760", "table", table, "proto", l2tpRouteProtocol); err != nil {
			return err
		}
		for _, private := range []string{"10.0.0.0/8", "172.16.0.0/12", "192.168.0.0/16", "169.254.0.0/16"} {
			if err := r.command("-4", "route", "replace", "throw", private, "table", table, "proto", l2tpRouteProtocol); err != nil {
				return err
			}
		}
		if next.Up {
			if err := r.command("-4", "route", "replace", "default", "dev", next.Device, "metric", "10", "table", table, "proto", l2tpRouteProtocol); err != nil {
				return err
			}
		} else {
			if err := r.remove("-4", "route", "del", "default", "metric", "10", "table", table, "proto", l2tpRouteProtocol); err != nil {
				return err
			}
		}
	}
	if changed {
		for _, old := range r.current {
			if err := r.deleteRules(old); err != nil {
				return err
			}
		}
		for _, next := range plan {
			table := l2tpTable(next.ID)
			for _, source := range next.Sources {
				if err := r.command("-4", "rule", "add", "pref", l2tpRulePriority, "from", source, "table", table, "protocol", l2tpRouteProtocol); err != nil {
					return err
				}
			}
			if next.Up {
				if err := r.command("-4", "rule", "add", "pref", l2tpRulePriority, "oif", next.Device, "table", table, "protocol", l2tpRouteProtocol); err != nil {
					return err
				}
			}
		}
		for _, old := range r.current {
			if _, retained := tables[l2tpTable(old.ID)]; !retained {
				if err := r.remove("-4", "route", "flush", "table", l2tpTable(old.ID), "proto", l2tpRouteProtocol); err != nil {
					return err
				}
			}
		}
		if err := r.save(plan); err != nil {
			return err
		}
		r.current = plan
		r.dirty = false
	}
	return nil
}
