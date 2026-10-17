#!/bin/sh
# Upgrade a stopped pre-L2TP installation without loading the global firewall
# or starting OpenPass. Every nft command is redirected to an isolated mock.
set -eu
ROOT=$(CDPATH= cd -- "$(dirname -- "$0")/../.." && pwd)
python3 - "$ROOT" <<'PY'
import json
import os
from pathlib import Path
import shlex
import subprocess
import sys
import tempfile

root = Path(sys.argv[1])
shell = os.environ.get("OPENPASS_TEST_SH", "sh")
with tempfile.TemporaryDirectory(prefix="openpass-l2tp-upgrade-test.") as directory:
    work = Path(directory)
    (work / "bin").mkdir()
    helper = work / "mock.py"
    helper.write_text(r'''
import json, os, sys
from pathlib import Path
root = Path(os.environ["TEST_ROOT"])
mode, *args = sys.argv[1:]
state_file = root / "firewall.json"
state = json.loads(state_file.read_text())
if mode == "jsonfilter":
    assert args == ["-e", '@.nftables[@.rule.comment="openpass-l2tp-forward"].rule.handle'], args
    values = [x["rule"]["handle"] for x in json.load(sys.stdin)["nftables"]
              if x.get("rule", {}).get("comment") == "openpass-l2tp-forward"]
    for value in values: print(value)
    sys.exit(0 if values else 1)
assert mode == "nft", mode
if args == ["list", "table", "inet", "fw4"]:
    sys.exit(1 if not state["active"] or os.environ.get("TEST_NFT_UNAVAILABLE") else 0)
if args == ["list", "ruleset"]:
    sys.exit(1 if os.environ.get("TEST_NFT_UNAVAILABLE") else 0)
if args == ["-j", "-a", "list", "chain", "inet", "fw4", "forward"]:
    print(json.dumps({"nftables": [{"rule": value} for value in state["forward"]]}))
    sys.exit(0)
assert args == ["-f", "-"], args
commands = sys.stdin.read().splitlines()
with (root / "transactions").open("a") as log: log.write(json.dumps(commands) + "\n")
if os.environ.get("TEST_NFT_APPLY_FAIL"): sys.exit(1)
for command in commands:
    if command == "add chain inet fw4 openpass_l2tp_forward":
        state["chains"].setdefault("openpass_l2tp_forward", [])
    elif command == "add chain inet fw4 openpass_l2tp_nat { type nat hook postrouting priority srcnat + 1; policy accept; }":
        state["chains"].setdefault("openpass_l2tp_nat", [])
    elif command == 'insert rule inet fw4 forward jump openpass_l2tp_forward comment "openpass-l2tp-forward"':
        state["forward"].insert(0, {"comment": "openpass-l2tp-forward", "handle": 42, "jump": "openpass_l2tp_forward"})
    else: raise AssertionError("Touched an unrelated object: " + command)
state_file.write_text(json.dumps(state))
''')
    for name in ["nft", "jsonfilter"]:
        target = work / "bin" / name
        target.write_text("#!/bin/sh\nexec " + shlex.quote(Path(sys.executable).as_posix()) + " " + shlex.quote(helper.as_posix()) + " " + name + ' "$@"\n')
        target.chmod(0o755)
    original = {"active": True, "chains": {"openpass_forward": [], "other_proxy": ["preserve"]},
                "forward": [{"comment": "other-proxy", "handle": 7, "jump": "other_proxy"}]}
    state_file = work / "firewall.json"
    state_file.write_text(json.dumps(original))
    marker = work / "service-disabled"
    marker.write_text("stopped\n")
    env = dict(os.environ, TEST_ROOT=work.as_posix(), PATH=str(work / "bin") + os.pathsep + os.environ["PATH"])
    def invoke(**overrides):
        return subprocess.run([shell, "-c", '. "$1"; openpass_prepare_l2tp_firewall', "test", (root / "scripts/firewall-l2tp.sh").as_posix()],
                              text=True, capture_output=True, env=dict(env, **overrides), timeout=15)
    result = invoke()
    assert result.returncode == 0, result.stderr
    upgraded = json.loads(state_file.read_text())
    assert upgraded["chains"]["openpass_l2tp_forward"] == []
    assert upgraded["chains"]["openpass_l2tp_nat"] == []
    assert upgraded["chains"]["other_proxy"] == ["preserve"]
    assert upgraded["forward"][1:] == original["forward"]
    assert upgraded["forward"][0]["jump"] == "openpass_l2tp_forward"
    assert marker.read_text() == "stopped\n"
    # Applying it again cannot duplicate the jump or flush existing rules.
    upgraded["chains"]["openpass_l2tp_forward"] = ["preserve-own-rule"]
    state_file.write_text(json.dumps(upgraded))
    result = invoke()
    assert result.returncode == 0, result.stderr
    assert json.loads(state_file.read_text()) == upgraded
    # An nft failure must be surfaced; the transaction leaves every rule intact.
    result = invoke(TEST_NFT_APPLY_FAIL="1")
    assert result.returncode != 0
    assert json.loads(state_file.read_text()) == upgraded
    # A stopped firewall is not replaced by a partial fw4 table. But failure to
    # inspect the firewall itself cannot be mistaken for an absent table.
    upgraded["active"] = False
    state_file.write_text(json.dumps(upgraded))
    before = (work / "transactions").read_text()
    assert invoke().returncode == 0
    assert (work / "transactions").read_text() == before
    assert invoke(TEST_NFT_UNAVAILABLE="1").returncode != 0
    installer = (root / "scripts/install.sh").read_text()
    stopped = installer.split("if [ -e /etc/openpass/service-disabled ]; then", 1)[1].split("\n\telse", 1)[0]
    assert "openpass_prepare_l2tp_firewall" in stopped
    assert stopped.index("/etc/init.d/openpass stop") < stopped.index("openpass_prepare_l2tp_firewall")
    assert "firewall reload" not in stopped
print("PASS: stopped upgrades add only empty OpenPass L2TP objects, preserve other rules and stopped state, and handle failures without a global reload.")
PY
