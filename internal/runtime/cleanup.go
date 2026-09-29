package runtime

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"time"
)

// CleanupDisabled is the post-procd cleanup path, including after an unclean
// daemon exit. It never reads or changes saved device/node settings and never
// terminates a sing-box instance belonging to another application.
func (rt *Runtime) CleanupDisabled() error {
	if !rt.Disabled() {
		return fmt.Errorf("refusing cleanup: OpenPass service is not disabled")
	}
	deadline := time.Now().Add(35 * time.Second)
	for {
		pids, err := matchingProcesses(func(args []string) bool {
			return len(args) > 0 && filepath.Base(args[0]) == "openpassd" &&
				!hasFlag(args, "cleanup") && flagValue(args, "config", "/var/etc/openpass/sing-box.json") == rt.ConfigPath
		})
		if err != nil {
			return err
		}
		if len(pids) == 0 {
			break
		}
		if time.Now().After(deadline) {
			return fmt.Errorf("OpenPass daemon has not stopped; firewall cleanup deferred")
		}
		time.Sleep(100 * time.Millisecond)
	}
	// HTTP cancellation normally stops probes. Handle an orphaned probe or
	// core after SIGKILL too, using only OpenPass-owned configuration paths.
	pids, err := matchingProcesses(rt.ownsCore)
	if err != nil {
		return err
	}
	for _, pid := range pids {
		process, err := os.FindProcess(pid)
		if err == nil {
			_ = process.Signal(syscall.SIGTERM)
		}
	}
	deadline = time.Now().Add(6 * time.Second)
	for {
		pids, err = matchingProcesses(rt.ownsCore)
		if err != nil || len(pids) == 0 {
			break
		}
		if time.Now().After(deadline) {
			// Do not claim successful cleanup after force-killing a TUN core:
			// its generic sing-box routing table may be shared by another app.
			// A visible failure is safer than deleting another app's rules.
			return fmt.Errorf("OpenPass sing-box has not completed shutdown; retry closing the service")
		}
		time.Sleep(100 * time.Millisecond)
	}
	if err != nil {
		return err
	}
	return rt.clearFirewall()
}

func (rt *Runtime) ownsCore(args []string) bool {
	if len(args) < 2 || filepath.Base(args[0]) != filepath.Base(rt.SingBoxPath) || args[1] != "run" {
		return false
	}
	config := flagValue(args, "c", "")
	if config == "" {
		config = flagValue(args, "config", "")
	}
	if config == rt.ConfigPath {
		return true
	}
	return filepath.Base(config) == "sing-box.json" &&
		filepath.Dir(filepath.Dir(config)) == os.TempDir() &&
		strings.HasPrefix(filepath.Base(filepath.Dir(config)), "openpass-probe-")
}

func hasFlag(args []string, name string) bool {
	for _, arg := range args[1:] {
		if strings.TrimLeft(arg, "-") == name || strings.HasPrefix(strings.TrimLeft(arg, "-"), name+"=") {
			return true
		}
	}
	return false
}

func flagValue(args []string, name, fallback string) string {
	for i := 1; i < len(args); i++ {
		arg := strings.TrimLeft(args[i], "-")
		if arg == name && i+1 < len(args) {
			return args[i+1]
		}
		if strings.HasPrefix(arg, name+"=") {
			return strings.TrimPrefix(arg, name+"=")
		}
	}
	return fallback
}

func matchingProcesses(match func([]string) bool) ([]int, error) {
	entries, err := os.ReadDir("/proc")
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil // Non-Linux development hosts have no router processes.
	}
	if err != nil {
		return nil, fmt.Errorf("inspect OpenPass processes: %w", err)
	}
	var pids []int
	for _, entry := range entries {
		pid, err := strconv.Atoi(entry.Name())
		if err != nil || pid == os.Getpid() {
			continue
		}
		data, err := os.ReadFile(filepath.Join("/proc", entry.Name(), "cmdline"))
		if err != nil || len(data) == 0 {
			continue // Process exited or is already a zombie.
		}
		if match(strings.Split(strings.TrimRight(string(data), "\x00"), "\x00")) {
			pids = append(pids, pid)
		}
	}
	return pids, nil
}

// clearFirewall empties only OpenPass policies. Empty include files are safe
// during fw4 reload and after reboot, even when the hook chains do not exist.
func (rt *Runtime) clearFirewall() error {
	for _, path := range []string{rt.FallbackNFTPath, rt.NFTPath} {
		if path != "" {
			if err := writePolicyFile(path, "# OpenPass service is disabled.\n"); err != nil {
				return err
			}
		}
	}
	nft, err := exec.LookPath("nft")
	if err != nil {
		return nil
	}
	// Discover existing objects first: cleanup must also work with no fw4
	// table or only some chains installed, without a global firewall reload.
	output, err := exec.Command(nft, "-j", "list", "ruleset").CombinedOutput()
	if err != nil {
		return fmt.Errorf("inspect nftables during shutdown: %s: %w", strings.TrimSpace(string(output)), err)
	}
	commands, err := cleanupCommands(output)
	if err != nil || commands == "" {
		return err
	}
	cmd := exec.Command(nft, "-f", "-")
	cmd.Stdin = strings.NewReader(commands)
	if output, err := cmd.CombinedOutput(); err != nil {
		return fmt.Errorf("clear OpenPass nftables policy: %s: %w", strings.TrimSpace(string(output)), err)
	}
	return nil
}

func cleanupCommands(ruleset []byte) (string, error) {
	type object struct {
		Family string `json:"family"`
		Table  string `json:"table"`
		Name   string `json:"name"`
	}
	var rules struct {
		NFTables []map[string]json.RawMessage `json:"nftables"`
	}
	if err := json.Unmarshal(ruleset, &rules); err != nil {
		return "", fmt.Errorf("decode nftables state: %w", err)
	}
	chains := map[string]bool{"openpass_prerouting": true, "openpass_output": true, "openpass_forward": true, "openpass_dns_nat": true, "openpass_dns_output": true}
	sets := map[string]bool{"proxy_clients": true, "direct_clients": true, "blocked_clients": true}
	var b bytes.Buffer
	for _, item := range rules.NFTables {
		for _, kind := range []string{"chain", "set"} {
			var obj object
			if data, ok := item[kind]; !ok || json.Unmarshal(data, &obj) != nil || obj.Family != "inet" || obj.Table != "fw4" {
				continue
			}
			if kind == "chain" && chains[obj.Name] || kind == "set" && sets[obj.Name] {
				fmt.Fprintf(&b, "flush %s inet fw4 %s\n", kind, obj.Name)
			}
		}
	}
	return b.String(), nil
}
