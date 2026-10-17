package runtime

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"os"
	"os/exec"
	"strconv"
	"strings"
	"time"

	"openpass/internal/l2tp"
	"openpass/internal/model"
)

func hasL2TP(nodes []model.Node) bool {
	for _, n := range nodes {
		if n.Type == "l2tp" {
			return true
		}
	}
	return false
}

func (rt *Runtime) l2tpLocked() *l2tp.Manager {
	if rt.l2tpManager == nil {
		rt.l2tpManager = l2tp.New()
	}
	return rt.l2tpManager
}

func (rt *Runtime) L2TPDependencies() l2tp.Dependencies {
	rt.mu.Lock()
	defer rt.mu.Unlock()
	return rt.l2tpDependenciesLocked()
}

// L2TPModuleError is an in-memory result of the last Apply. Reading it never
// starts dependency checks or netifd queries while the module is disabled.
func (rt *Runtime) L2TPModuleError() string {
	rt.mu.Lock()
	defer rt.mu.Unlock()
	return rt.l2tpModuleError
}

func (rt *Runtime) l2tpDependenciesLocked() l2tp.Dependencies {
	d := rt.l2tpLocked().Dependencies()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	output, err := exec.CommandContext(ctx, rt.SingBoxPath, "version").CombinedOutput()
	valid := false
	for _, line := range strings.Split(string(output), "\n") {
		fields := strings.Fields(line)
		if len(fields) < 3 || fields[0] != "sing-box" || fields[1] != "version" {
			continue
		}
		parts := strings.Split(fields[2], ".")
		if len(parts) >= 2 {
			major, _ := strconv.Atoi(parts[0])
			minor, _ := strconv.Atoi(parts[1])
			valid = major == 1 && minor >= 14
		}
	}
	if err != nil || !valid {
		d.Ready = false
		d.Missing = append(d.Missing, "sing-box >= 1.14（L2TP 设备排除）")
	}
	ipOutput, ipErr := exec.CommandContext(ctx, "ip", "-j", "-4", "rule", "show").CombinedOutput()
	var rules []json.RawMessage
	if ipErr != nil || json.Unmarshal(ipOutput, &rules) != nil {
		d.Ready = false
		d.Missing = append(d.Missing, "ip-full")
	}
	return d
}

func (rt *Runtime) validateL2TP(st model.State) error {
	if !st.Settings.Enabled || st.Settings.L2TPDisabled {
		return nil
	}
	for _, n := range st.Nodes {
		if n.Type == "l2tp" && n.Enabled {
			deps := rt.l2tpDependenciesLocked()
			if !deps.Ready {
				return fmt.Errorf("L2TP 缺少依赖：%s", strings.Join(deps.Missing, "、"))
			}
			break
		}
	}
	return nil
}

func (rt *Runtime) L2TPStatuses(nodes []model.Node) map[string]l2tp.Status {
	rt.mu.Lock()
	defer rt.mu.Unlock()
	if rt.l2tpState.Settings.L2TPDisabled {
		statuses := map[string]l2tp.Status{}
		for _, n := range nodes {
			if n.Type == "l2tp" {
				statuses[n.ID] = l2tp.Status{State: "disabled", Interface: l2tp.InterfaceName(n.ID)}
			}
		}
		return statuses
	}
	statuses := rt.l2tpLocked().Statuses(nodes)
	{
		// A negotiated PPP session is not usable until its isolated route has
		// been installed. Never paint a broken routing setup as connected.
		for id, status := range statuses {
			if !status.Connected {
				continue
			}
			ready := false
			if rt.l2tpRoutes != nil && !rt.l2tpRoutes.dirty {
				for _, r := range rt.l2tpRoutes.current {
					if r.ID == id && r.Up {
						ready = true
					}
				}
			}
			if !ready {
				status.State = "connecting"
				status.Connected = false
				status.LastError = "隧道已建立，正在配置设备路由"
				statuses[id] = status
			}
		}
	}
	return statuses
}

func (rt *Runtime) ReconnectL2TP(id string) error {
	rt.mu.Lock()
	defer rt.mu.Unlock()
	if rt.closing.Load() || rt.Disabled() {
		return fmt.Errorf("OpenPass 已关闭")
	}
	if rt.l2tpState.Settings.L2TPDisabled {
		return fmt.Errorf("请先开启 L2TP 功能")
	}
	if err := rt.l2tpLocked().Reconnect(id); err != nil {
		return err
	}
	return rt.refreshL2TPRoutesLocked()
}

func (rt *Runtime) configureL2TPLocked(st model.State) error {
	rt.l2tpState = st
	if st.Settings.L2TPDisabled {
		// Saved nodes must not restart the watcher while the module is off.
		return rt.cleanupL2TPLocked()
	}
	if !st.Settings.Enabled || !hasL2TP(st.Nodes) {
		rt.stopL2TPWatcherLocked()
	}
	// Avoid adding router dependencies to existing installations with no L2TP.
	if !hasL2TP(st.Nodes) && rt.l2tpManager == nil {
		if _, err := os.Stat("/var/run/openpass/l2tp-routes.json"); os.IsNotExist(err) {
			return nil
		}
	}
	manager := rt.l2tpLocked()
	if err := manager.Reconcile(st.Nodes, st.Settings.Enabled); err != nil {
		return err
	}
	if rt.l2tpRoutes == nil {
		rt.l2tpRoutes = newL2TPRoutes()
	}
	if err := rt.refreshL2TPRoutesLocked(); err != nil {
		return err
	}
	if rt.l2tpStop == nil && hasL2TP(st.Nodes) && st.Settings.Enabled {
		rt.l2tpStop = make(chan struct{})
		go rt.watchL2TP(rt.l2tpStop)
	}
	return nil
}

func (rt *Runtime) refreshL2TPRoutesLocked() error {
	if rt.l2tpState.Settings.L2TPDisabled {
		return nil
	}
	statuses := rt.l2tpLocked().Statuses(rt.l2tpState.Nodes)
	return rt.l2tpRoutes.sync(l2tpRoutePlan(rt.l2tpState, statuses))
}

func (rt *Runtime) watchL2TP(stop <-chan struct{}) {
	ticker := time.NewTicker(3 * time.Second)
	defer ticker.Stop()
	lastError := ""
	ticks := 0
	for {
		select {
		case <-stop:
			return
		case <-ticker.C:
		}
		rt.mu.Lock()
		// An old watcher can wake while Apply holds the mutex. Check its
		// own stop channel again after locking, before querying netifd.
		select {
		case <-stop:
			rt.mu.Unlock()
			return
		default:
		}
		if rt.closing.Load() || rt.Disabled() || rt.l2tpState.Settings.L2TPDisabled || !rt.l2tpState.Settings.Enabled {
			rt.mu.Unlock()
			return
		}
		ticks++
		var err error
		if ticks%10 == 0 && rt.l2tpState.Settings.Enabled {
			err = rt.l2tpLocked().Reconcile(rt.l2tpState.Nodes, true)
		}
		if routeErr := rt.refreshL2TPRoutesLocked(); routeErr != nil {
			err = routeErr
		}
		rt.mu.Unlock()
		if err != nil {
			if err.Error() != lastError {
				log.Printf("L2TP routes: %v", err)
				lastError = err.Error()
			}
		} else {
			lastError = ""
		}
	}
}

func (rt *Runtime) cleanupL2TPLocked() error {
	rt.stopL2TPWatcherLocked()
	// The manager scopes discovery to explicitly-owned UCI interfaces; this
	// also handles a daemon killed before it could record in-memory state.
	if err := rt.l2tpLocked().Cleanup(true); err != nil {
		return err
	}
	if rt.l2tpRoutes == nil {
		rt.l2tpRoutes = newL2TPRoutes()
	}
	return rt.l2tpRoutes.sync(nil)
}

func (rt *Runtime) stopL2TPWatcherLocked() {
	if rt.l2tpStop != nil {
		close(rt.l2tpStop)
		rt.l2tpStop = nil
	}
}
