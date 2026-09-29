#!/bin/sh
# Run the real rpcd shell entry point in a temporary filesystem. No host
# services, firewall, or router are accessed; flock remains the real OS lock.
set -eu
ROOT=$(CDPATH= cd -- "$(dirname -- "$0")/../.." && pwd)
python3 - "$ROOT" <<'PY'
import fcntl
import json
import os
from pathlib import Path
import shutil
import subprocess
import sys
import tempfile

source = Path(sys.argv[1]) / "luci-app-openpass/root/usr/libexec/rpcd/openpass"
assert shutil.which("flock"), "flock is required for the concurrency regression"

with tempfile.TemporaryDirectory(prefix="openpass-service-test.") as directory:
    work = Path(directory)
    bin_dir = work / "bin"
    bin_dir.mkdir()
    for path in ["etc/init.d", "etc/openpass", "var/lock", "var/run/openpass", "usr/share/libubox"]:
        (work / path).mkdir(parents=True, exist_ok=True)
    marker = work / "etc/openpass/service-disabled"
    enabled = work / "enabled"
    running = work / "running"
    actions = work / "actions"
    helper = work / "mock.py"
    helper.write_text(r'''
import json
import os
from pathlib import Path
import sys

root = Path(os.environ["TEST_ROOT"])
mode, *args = sys.argv[1:]
if mode == "load":
    try:
        value = json.loads(os.environ["JSHN_INPUT"])
        if not isinstance(value, dict):
            raise ValueError("expected object")
    except (ValueError, KeyError):
        sys.exit(1)
elif mode == "type":
    value = json.loads(os.environ["JSHN_INPUT"]).get(args[0])
    print("boolean" if type(value) is bool else "invalid")
elif mode == "value":
    value = json.loads(os.environ["JSHN_INPUT"])[args[0]]
    print(1 if value is True else 0)
elif mode == "add":
    path = root / "response"
    value = json.loads(path.read_text())
    value[args[1]] = args[2] == "1" if args[0] == "boolean" else args[2]
    path.write_text(json.dumps(value))
elif mode == "ubus":
    if args != ["call", "service", "list", '{"name":"openpass"}']:
        raise SystemExit("Unexpected ubus target: " + repr(args))
    print(json.dumps({"openpass": {"instances": {"openpass": {"running": (root / "running").exists()}}}}))
elif mode == "jsonfilter":
    if args != ["-e", "@.openpass.instances.openpass.running"]:
        raise SystemExit("Unexpected jsonfilter expression: " + repr(args))
    value = json.load(sys.stdin)
    print(str(value["openpass"]["instances"]["openpass"]["running"]).lower())
elif mode == "init":
    if len(args) != 1 or args[0] not in {"enable", "disable", "start", "stop", "enabled", "running"}:
        raise SystemExit("Unexpected service operation: " + repr(args))
    action = args[0]
    marker = root / "etc/openpass/service-disabled"
    state = marker.read_text().strip() if marker.exists() else "absent"
    if action in {"enabled", "running"}:
        sys.exit(0 if (root / action).exists() else 1)
    with (root / "actions").open("a") as stream:
        stream.write(action + " " + state + "\n")
    if os.environ.get("TEST_FAIL_ACTION") == action:
        sys.exit(1)
    target = root / ("enabled" if action in {"enable", "disable"} else "running")
    if action in {"enable", "start"}:
        target.touch()
    else:
        target.unlink(missing_ok=True)
else:
    raise SystemExit("Unexpected helper operation")
''')
    jshn = work / "usr/share/libubox/jshn.sh"
    jshn.write_text('''
json_load() { JSHN_INPUT=$1; export JSHN_INPUT; python3 "$TEST_ROOT/mock.py" load; }
json_get_type() { eval "$1=\\$(python3 \\"\\$TEST_ROOT/mock.py\\" type \\"\\$2\\")"; }
json_get_var() { eval "$1=\\$(python3 \\"\\$TEST_ROOT/mock.py\\" value \\"\\$2\\")"; }
json_init() { printf '%s' '{}' >"$TEST_ROOT/response"; }
json_add_boolean() { python3 "$TEST_ROOT/mock.py" add boolean "$1" "$2"; }
json_add_string() { python3 "$TEST_ROOT/mock.py" add string "$1" "$2"; }
json_dump() { cat "$TEST_ROOT/response"; printf '\\n'; }
''')
    for name, mode in [("ubus", "ubus"), ("jsonfilter", "jsonfilter")]:
        path = bin_dir / name
        path.write_text('#!/bin/sh\nexec python3 "$TEST_ROOT/mock.py" ' + mode + ' "$@"\n')
        path.chmod(0o755)
    init = work / "etc/init.d/openpass"
    init.write_text('#!/bin/sh\nexec python3 "$TEST_ROOT/mock.py" init "$@"\n')
    init.chmod(0o755)
    rpc = work / "rpc"
    script = source.read_text()
    for original in ["/usr/share/libubox/jshn.sh", "/etc/init.d/openpass", "/etc/openpass", "/var/lock", "/var/run/openpass"]:
        script = script.replace(original, str(work) + original)
    rpc.write_text(script)
    env = dict(os.environ, TEST_ROOT=str(work), PATH=str(bin_dir) + os.pathsep + os.environ["PATH"])

    def invoke(method, payload=None, fail_action=None):
        local_env = dict(env)
        if fail_action:
            local_env["TEST_FAIL_ACTION"] = fail_action
        text = payload if isinstance(payload, str) else json.dumps(payload or {})
        result = subprocess.run(["sh", str(rpc), "call", method], input=text + "\n", text=True,
                                capture_output=True, env=local_env, timeout=15)
        assert result.returncode == 0, (method, result.returncode, result.stderr)
        try:
            return json.loads(result.stdout)
        except ValueError as exc:
            raise AssertionError((result.stdout, result.stderr)) from exc

    def log():
        return actions.read_text().splitlines() if actions.exists() else []

    def expect_error(result, context):
        assert result.get("error"), (context, result)

    enabled.touch()
    running.touch()
    for payload in [{}, {"enabled": "false"}, {"enabled": 0}, {"enabled": 1},
                    {"enabled": None}, {"enabled": []}, {"enabled": {}}, '{"enabled":']:
        expect_error(invoke("set_enabled", payload), "invalid enabled value")
        assert not marker.exists() and enabled.exists() and running.exists() and log() == []
    expect_error(invoke("arbitrary_command", {"enabled": False}), "unsupported operation")
    assert log() == []

    # The marker must precede every service mutation, and a completed stop must
    # remain queryable even though the OpenPass daemon/API is no longer running.
    response = invoke("set_enabled", {"enabled": False})
    assert "error" not in response, response
    assert log() == ["disable pending", "stop pending"], log()
    assert marker.read_text().strip() == "stopped"
    assert not enabled.exists() and not running.exists()
    response = invoke("status")
    assert response["running"] is False and response["enabled"] is False and response["stopped"] is True, response
    assert not response.get("cleanup_pending", False), response

    # Caller-supplied commands cannot influence the fixed service target.
    actions.write_text("")
    response = invoke("set_enabled", {"enabled": True, "command": "touch /should-not-run", "service": "other-proxy"})
    assert "error" not in response, response
    assert log() == ["enable absent", "start absent"], log()
    assert not marker.exists() and enabled.exists() and running.exists()
    assert response["running"] is True and response["enabled"] is True and response["stopped"] is False, response

    # A competing controller must not change persisted intent or service state.
    actions.write_text("")
    with (work / "var/lock/openpass-control.lock").open("a") as lock:
        fcntl.flock(lock.fileno(), fcntl.LOCK_EX | fcntl.LOCK_NB)
        expect_error(invoke("set_enabled", {"enabled": False}), "concurrent operation")
        assert log() == [] and not marker.exists() and enabled.exists() and running.exists()

    # Cleanup failures keep the stop intent across retries and must not report
    # success while the mock daemon is still running.
    response = invoke("set_enabled", {"enabled": False}, fail_action="stop")
    expect_error(response, "stop failure")
    assert marker.read_text().strip() == "pending"
    assert not enabled.exists() and running.exists()
    response = invoke("status")
    assert response["stopped"] is True and response["running"] is True, response
    if "cleanup_pending" in response:
        assert response["cleanup_pending"] is True, response
    response = invoke("set_enabled", {"enabled": False})
    assert "error" not in response and not running.exists(), response
    assert marker.read_text().strip() == "stopped"

    # A boot-registration failure must not prevent the best-effort runtime
    # shutdown, and it must keep a retryable marker instead of claiming success.
    invoke("set_enabled", {"enabled": True})
    actions.write_text("")
    response = invoke("set_enabled", {"enabled": False}, fail_action="disable")
    expect_error(response, "disable failure")
    assert log() == ["disable pending", "stop pending"], log()
    assert marker.read_text().strip() == "pending"
    assert enabled.exists() and not running.exists()
    response = invoke("set_enabled", {"enabled": False})
    assert "error" not in response and not enabled.exists() and not running.exists(), response
    assert marker.read_text().strip() == "stopped"

print("PASS: LuCI service control validates input, preserves stop intent, remains available while stopped, locks concurrent changes, and reports/retries cleanup failures.")
PY
