#!/bin/sh
# All router paths are redirected into a private temporary directory. Only
# real OS flock/setsid are used; no router, host service, or firewall is touched.
set -eu
ROOT=$(CDPATH= cd -- "$(dirname -- "$0")/../.." && pwd)
python3 - "$ROOT" <<'PY'
import fcntl
import json
import os
from pathlib import Path
import re
import shutil
import subprocess
import sys
import tempfile
import time

root = Path(sys.argv[1])
assert shutil.which("flock") and shutil.which("setsid")
with tempfile.TemporaryDirectory(prefix="openpass-maintenance-test.") as directory:
    work = Path(directory)
    for name in ["bin", "etc/init.d", "etc/openpass", "etc/rc.d", "usr/bin", "usr/share/openpass", "usr/share/libubox",
                 "usr/share/luci/menu.d", "usr/share/rpcd/acl.d", "usr/libexec/rpcd", "usr/share/nftables.d/ruleset-post", "usr/share/nftables.d/chain-pre/forward",
                 "www/luci-static/resources/view/openpass", "etc/nftables.d", "var/lock", "var/run/openpass", "var/etc/openpass", "tmp"]:
        (work / name).mkdir(parents=True, exist_ok=True)
    def patched(path):
        return re.sub(r"/(etc|usr|www|var|tmp)/", lambda m: str(work) + m.group(0), path.read_text())
    rpc = work / "rpc"
    rpc.write_text(patched(root / "luci-app-openpass/root/usr/libexec/rpcd/openpass"))
    helper = work / "mock.py"
    helper.write_text(r'''
import json, os, sys
from pathlib import Path
root = Path(os.environ["TEST_ROOT"])
mode, *args = sys.argv[1:]
def action(text):
    with (root / "actions").open("a") as f: f.write(text + "\n")
if mode == "load":
    try:
        value = json.loads(os.environ["JSHN_INPUT"])
        assert isinstance(value, dict)
    except Exception: sys.exit(1)
elif mode == "type":
    values = json.loads(os.environ["JSHN_INPUT"])
    if args[0] not in values: print("")
    else:
        value = values[args[0]]
        print("boolean" if type(value) is bool else "string" if isinstance(value, str) else "invalid")
elif mode == "value":
    value = json.loads(os.environ["JSHN_INPUT"]).get(args[0], "")
    print((1 if value else 0) if type(value) is bool else value)
elif mode == "add":
    path = root / ("response-" + os.environ["JSHN_RESPONSE"])
    value = json.loads(path.read_text())
    value[args[1]] = args[2] == "1" if args[0] == "boolean" else args[2]
    path.write_text(json.dumps(value))
elif mode == "daemon":
    if args == ["-version"]: print((root / "version").read_text().strip())
    elif args == ["-check-update"]:
        if os.environ.get("TEST_CHECK_FAIL"):
            print(json.dumps({"error": "network unavailable"})); sys.exit(1)
        print(json.dumps({"current_version": (root / "version").read_text().strip(), "latest_version": "0.1.99", "update_available": True}))
    elif args == ["-cleanup"]:
        action("cleanup")
        if os.environ.get("TEST_CLEANUP_FAIL"): sys.exit(1)
    else: raise SystemExit("Unexpected daemon args: " + repr(args))
elif mode == "init":
    assert len(args) == 1 and args[0] in {"enable", "disable", "start", "stop", "running", "enabled"}, args
    if args[0] in {"running", "enabled"}: sys.exit(0 if (root / args[0]).exists() else 1)
    action("init " + args[0])
    if os.environ.get("TEST_FAIL_ACTION") == args[0]: sys.exit(1)
    target = root / ("enabled" if args[0] in {"enable", "disable"} else "running")
    if args[0] in {"start", "enable"}: target.touch()
    else: target.unlink(missing_ok=True)
elif mode == "ubus":
    assert args == ["call", "service", "list", '{"name":"openpass"}'], args
    print(json.dumps({"openpass": {"instances": {"openpass": {"running": (root / "running").exists()}}}}))
elif mode == "jsonfilter":
    value = json.load(sys.stdin)
    if "nftables" in value:
        assert args == ["-e", '@.nftables[@.rule.comment="openpass-l2tp-forward"].rule.handle'], args
        found = False
        for item in value["nftables"]:
            if item.get("rule", {}).get("comment") == "openpass-l2tp-forward":
                print(item["rule"]["handle"]); found = True
        if not found: sys.exit(1)
    else: print(str(value["openpass"]["instances"]["openpass"]["running"]).lower())
elif mode == "id":
    assert args == ["-u"], args
    print(0)
elif mode == "ip":
    assert args == ["link", "show", "dev", "openpass0"], args
    sys.exit(0 if os.environ.get("TEST_TUN_REMAINS") else 1)
elif mode == "nft":
    if args == ["list", "ruleset"]: sys.exit(0)
    if args == ["-j", "-a", "list", "chain", "inet", "fw4", "forward"]:
        rules = [{"rule": {"comment": "other-proxy", "handle": 43}}]
        if not os.environ.get("TEST_NO_L2TP_JUMP"): rules.insert(0, {"rule": {"comment": "openpass-l2tp-forward", "handle": 42}})
        print(json.dumps({"nftables": rules}))
        sys.exit(0)
    if args == ["delete", "rule", "inet", "fw4", "forward", "handle", "42"]:
        action("nft " + " ".join(args))
        if os.environ.get("TEST_NFT_FAIL"): sys.exit(1)
        sys.exit(0)
    assert args[0] in {"list", "delete"} and args[1] in {"chain", "set"} and args[2:4] == ["inet", "fw4"], args
    assert args[4] in {"openpass_prerouting", "openpass_output", "openpass_forward", "openpass_dns_nat", "openpass_dns_output", "openpass_l2tp_forward", "openpass_l2tp_nat", "proxy_clients", "direct_clients", "blocked_clients"}, args
    if args[0] == "delete":
        action("nft " + " ".join(args))
        if os.environ.get("TEST_NFT_FAIL"): sys.exit(1)
else: raise SystemExit("Unexpected helper operation: " + mode)
''')
    (work / "usr/share/libubox/jshn.sh").write_text('''
json_load() { JSHN_INPUT=$1; export JSHN_INPUT; python3 "$TEST_ROOT/mock.py" load || return 1; JSHN_RESPONSE=$$; export JSHN_RESPONSE; printf '%s' "$JSHN_INPUT" >"$TEST_ROOT/response-$JSHN_RESPONSE"; }
json_get_type() { eval "$1=\\$(python3 \\"\\$TEST_ROOT/mock.py\\" type \\"\\$2\\")"; }
json_get_var() { eval "$1=\\\"\\$(python3 \\"\\$TEST_ROOT/mock.py\\" value \\"\\$2\\")\\\""; }
json_init() { JSHN_RESPONSE=$$; export JSHN_RESPONSE; printf '%s' '{}' >"$TEST_ROOT/response-$JSHN_RESPONSE"; }
json_add_boolean() { python3 "$TEST_ROOT/mock.py" add boolean "$1" "$2"; }
json_add_string() { python3 "$TEST_ROOT/mock.py" add string "$1" "$2"; }
json_dump() { cat "$TEST_ROOT/response-$JSHN_RESPONSE"; printf '\\n'; }
''')
    for name, mode in [("bin/ubus", "ubus"), ("bin/jsonfilter", "jsonfilter"), ("bin/id", "id"), ("bin/ip", "ip"), ("bin/nft", "nft")]:
        path = work / name
        path.write_text('#!/bin/sh\nexec python3 "$TEST_ROOT/mock.py" ' + mode + ' "$@"\n')
        path.chmod(0o755)
    def install_fixture():
        for name, mode in [("etc/init.d/openpass", "init"), ("usr/bin/openpassd", "daemon")]:
            path = work / name
            path.write_text('#!/bin/sh\nexec python3 "$TEST_ROOT/mock.py" ' + mode + ' "$@"\n')
            path.chmod(0o755)
        for name in ["maintenance.sh", "uninstall.sh"]:
            (work / "usr/share/openpass" / name).write_text(patched(root / "scripts" / name))
        (work / "usr/share/openpass/update.sh").write_text('''#!/bin/sh
set -eu
[ ! -e /proc/self/fd/9 ] || exit 31
printf '%s' "$OPENPASS_VERSION" >"$TEST_ROOT/installer-version"
touch "$TEST_ROOT/installer-running"
while [ ! -e "$TEST_ROOT/allow-install" ]; do sleep 0.1; done
[ -z "${TEST_INSTALL_FAIL:-}" ] || exit 1
printf '%s' "${OPENPASS_VERSION#v}" >"$TEST_ROOT/version"
''')
        for name in ["etc/openwrt_release", "etc/nftables.d/90-openpass.nft", "usr/share/nftables.d/ruleset-post/92-openpass-policy.nft", "usr/share/nftables.d/chain-pre/forward/30-openpass-l2tp.nft",
                     "usr/share/luci/menu.d/luci-app-openpass.json", "usr/share/rpcd/acl.d/luci-app-openpass.json", "usr/libexec/rpcd/openpass",
                     "www/luci-static/resources/view/openpass/service.js", "var/etc/openpass/sing-box.json", "var/run/openpass/91-openpass-dynamic.nft"]:
            (work / name).write_text("fixture")
    install_fixture()
    (work / "usr/bin/sing-box").write_text("shared core")
    (work / "etc/init.d/other-proxy").write_text("other service")
    (work / "etc/openpass/state.json").write_text('{"nodes":["preserve"]}')
    (work / "version").write_text("0.1.12")
    (work / "enabled").touch()
    (work / "running").touch()
    env = dict(os.environ, TEST_ROOT=str(work), PATH=str(work / "bin") + os.pathsep + os.environ["PATH"])
    def invoke(method, payload=None, **overrides):
        result = subprocess.run(["sh", str(rpc), "call", method], input=json.dumps(payload or {}) + "\n", text=True,
                                capture_output=True, env=dict(env, **overrides), timeout=20)
        assert result.returncode == 0, (method, result.returncode, result.stderr)
        try: return json.loads(result.stdout)
        except ValueError: raise AssertionError((result.stdout, result.stderr))
    def wait_for(check, timeout=15):
        deadline = time.monotonic() + timeout
        while time.monotonic() < deadline:
            if check(): return
            time.sleep(0.1)
        raise AssertionError("timed out")
    def lock_free():
        with (work / "var/lock/openpass-control.lock").open("a") as fd:
            try: fcntl.flock(fd.fileno(), fcntl.LOCK_EX | fcntl.LOCK_NB); return True
            except BlockingIOError: return False
    assert invoke("status")["version"] == "0.1.12"
    assert invoke("check_update")["latest_version"] == "0.1.99"
    assert invoke("check_update", TEST_CHECK_FAIL="1")["error"] == "network unavailable"
    for version in [None, True, 12, "latest", "v1.2", "v1.2.3;id", "v1.2.3\nv4.5.6", "v0.1.98", "../0.1.99"]:
        assert invoke("update", {"version": version}).get("error"), version
    for payload in [{}, {"confirm": False}, {"confirm": "true"}, {"confirm": True, "keep_config": "false"},
                    {"confirm": True, "keep_config": 0}, {"confirm": True, "keep_config": None}]:
        assert invoke("uninstall", payload).get("error"), payload
    assert not (work / "actions").exists()
    with (work / "var/lock/openpass-control.lock").open("a") as fd:
        fcntl.flock(fd.fileno(), fcntl.LOCK_EX | fcntl.LOCK_NB)
        for method, payload in [("update", {"version": "v0.1.99"}), ("check_update", {}),
                                ("uninstall", {"confirm": True}), ("set_enabled", {"enabled": False})]:
            assert invoke(method, payload).get("error"), method
    assert invoke("maintenance_status")["state"] == "idle"
    assert invoke("update", {"version": "v0.1.99"}) == {"accepted": True}
    wait_for(lambda: (work / "installer-running").exists())
    assert invoke("maintenance_status")["state"] == "running"
    assert invoke("set_enabled", {"enabled": False}).get("error")
    assert invoke("uninstall", {"confirm": True}).get("error")
    (work / "allow-install").touch()
    wait_for(lambda: invoke("maintenance_status")["state"] == "success")
    assert (work / "installer-version").read_text() == "v0.1.99"
    assert invoke("status")["version"] == "0.1.99"
    wait_for(lock_free)
    # Failure status is visible and the lock is eventually released for retry.
    assert invoke("update", {"version": "v0.1.99"}, TEST_INSTALL_FAIL="1") == {"accepted": True}
    wait_for(lambda: invoke("maintenance_status")["state"] == "error")
    wait_for(lock_free)
    assert invoke("status")["version"] == "0.1.99"
    status_file = work / "tmp/openpass-maintenance/status.json"
    status_file.write_text('{"state":"running","action":"update","version":"v0.1.99","message":""}')
    assert invoke("maintenance_status")["state"] == "error"
    # Cleanup errors keep every application component available for retry.
    for failure in ["TEST_CLEANUP_FAIL", "TEST_FAIL_ACTION", "TEST_TUN_REMAINS", "TEST_NFT_FAIL"]:
        (work / "actions").write_text("")
        response = invoke("uninstall", {"confirm": True, "keep_config": False}, **{failure: "stop" if failure == "TEST_FAIL_ACTION" else "1"})
        assert response.get("error"), (failure, response)
        if failure == "TEST_FAIL_ACTION":
            assert "cleanup" not in (work / "actions").read_text().splitlines(), "failed stop must not repeat long cleanup"
        for name in ["usr/bin/openpassd", "usr/share/openpass/uninstall.sh", "usr/share/luci/menu.d/luci-app-openpass.json",
                     "etc/nftables.d/90-openpass.nft", "etc/openpass/state.json"]:
            assert (work / name).exists(), (failure, name)
    response = invoke("uninstall", {"confirm": True})
    assert response == {"uninstalled": True, "config_kept": True}, response
    cleanup_actions = (work / "actions").read_text().splitlines()
    assert "nft delete rule inet fw4 forward handle 42" in cleanup_actions
    assert "nft delete rule inet fw4 forward handle 43" not in cleanup_actions
    assert cleanup_actions.index("nft delete rule inet fw4 forward handle 42") < cleanup_actions.index("nft delete chain inet fw4 openpass_l2tp_forward")
    assert (work / "etc/openpass/state.json").read_text() == '{"nodes":["preserve"]}'
    for name in ["usr/bin/openpassd", "usr/share/openpass", "usr/share/luci/menu.d/luci-app-openpass.json", "usr/libexec/rpcd/openpass",
                 "www/luci-static/resources/view/openpass", "etc/nftables.d/90-openpass.nft", "usr/share/nftables.d/ruleset-post/92-openpass-policy.nft", "usr/share/nftables.d/chain-pre/forward/30-openpass-l2tp.nft"]:
        assert not (work / name).exists(), name
    assert (work / "usr/bin/sing-box").read_text() == "shared core"
    assert (work / "etc/init.d/other-proxy").read_text() == "other service"
    # Reinstall the fixture and exercise explicit configuration deletion.
    for name in ["usr/share/openpass", "www/luci-static/resources/view/openpass", "var/run/openpass", "var/etc/openpass"]:
        (work / name).mkdir(parents=True, exist_ok=True)
    install_fixture()
    response = invoke("uninstall", {"confirm": True, "keep_config": False}, TEST_NO_L2TP_JUMP="1")
    assert response == {"uninstalled": True, "config_kept": False}, response
    assert not (work / "etc/openpass").exists()
    assert (work / "usr/bin/sing-box").exists() and (work / "etc/init.d/other-proxy").exists()
print("PASS: maintenance validates arguments, detaches and locks updates, reports failures, and uninstalls only after safe cleanup while preserving shared components.")
PY
