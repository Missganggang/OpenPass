#!/bin/sh
set -eu
ROOT=$(CDPATH= cd -- "$(dirname -- "$0")/../.." && pwd)
. "$ROOT/scripts/firewall-settings.sh"
work=$(mktemp -d "${TMPDIR:-/tmp}/openpass-fw4-settings-test.XXXXXX")
trap 'rm -rf "$work"' 0
trap 'exit 1' 1 2 15
mkdir "$work/bin" "$work/config"
MOCK_UCI_DIR="$work/config"
export MOCK_UCI_DIR
cat >"$work/bin/uci" <<'SH'
#!/bin/sh
[ "$1" != -q ] || shift
case "$1" in
	get)
		[ "$2" != 'firewall.@defaults[0]' ] || { echo defaults; exit 0; }
		cat "$MOCK_UCI_DIR/${2##*.}" 2>/dev/null ;;
	set)
		assignment=${2##*.}
		printf '%s\n' "${assignment#*=}" >"$MOCK_UCI_DIR/${assignment%%=*}" ;;
	commit) echo commit >>"$MOCK_UCI_DIR/commits" ;;
	*) exit 2 ;;
esac
SH
chmod 0755 "$work/bin/uci"
PATH="$work/bin:$PATH"
export PATH
echo 1 >"$work/config/flow_offloading"
echo 1 >"$work/config/flow_offloading_hw"
echo 0 >"$work/config/auto_includes"
openpass_configure_fw4_defaults "$work/backup"
test "$(cat "$work/config/flow_offloading")" = 0
test "$(cat "$work/config/flow_offloading_hw")" = 0
test "$(cat "$work/config/auto_includes")" = 1
test "$(stat -c %a "$work/backup")" = 600
grep -Fx 'flow_offloading=1' "$work/backup" >/dev/null
grep -Fx 'flow_offloading_hw=1' "$work/backup" >/dev/null
grep -Fx 'auto_includes=0' "$work/backup" >/dev/null
before=$(sha256sum "$work/backup")
openpass_configure_fw4_defaults "$work/backup"
test "$(wc -l <"$work/config/commits")" -eq 1
test "$before" = "$(sha256sum "$work/backup")"
echo 1 >"$work/config/flow_offloading"
openpass_configure_fw4_defaults "$work/backup"
test "$(wc -l <"$work/config/commits")" -eq 2
test "$before" = "$(sha256sum "$work/backup")"
rm "$work/config/flow_offloading" "$work/config/auto_includes"
openpass_configure_fw4_defaults "$work/unset-backup"
grep -Fx 'flow_offloading=<unset>' "$work/unset-backup" >/dev/null
grep -Fx 'auto_includes=<unset>' "$work/unset-backup" >/dev/null
echo 'PASS: firewall defaults recorded privately once; offloading disabled; automatic includes enabled; no commit for unchanged settings.'
