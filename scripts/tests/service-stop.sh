#!/bin/sh
# Verify the init lifecycle without touching the host's service manager.
set -eu
ROOT=$(CDPATH= cd -- "$(dirname "$0")/../.." && pwd)
work=$(mktemp -d "${TMPDIR:-/tmp}/openpass-service-test.XXXXXX")
trap 'rm -rf "$work"' EXIT INT TERM
mkdir -p "$work/etc/openpass" "$work/bin"
cat >"$work/bin/openpassd" <<'SH'
#!/bin/sh
printf '%s\n' "$*" >>"$OPENPASS_TEST_LOG"
exit "${OPENPASS_TEST_RESULT:-0}"
SH
chmod 0755 "$work/bin/openpassd"
sed -e "s|/etc/openpass|$work/etc/openpass|g" \
    -e "s|/var/run/openpass|$work/run/openpass|g" \
    -e "s|/usr/bin/openpassd|$work/bin/openpassd|g" \
    "$ROOT/openwrt/openpass.init" >"$work/init"
OPENPASS_TEST_LOG="$work/actions"
export OPENPASS_TEST_LOG
: >"$OPENPASS_TEST_LOG"
procd_open_instance() { printf 'open %s\n' "$*" >>"$OPENPASS_TEST_LOG"; }
procd_set_param() { printf 'param %s\n' "$*" >>"$OPENPASS_TEST_LOG"; }
procd_close_instance() { :; }
. "$work/init"
touch "$work/etc/openpass/service-disabled"
start_service
[ ! -s "$OPENPASS_TEST_LOG" ]
service_stopped
grep -Fx -- '-cleanup' "$OPENPASS_TEST_LOG" >/dev/null
OPENPASS_TEST_RESULT=7
export OPENPASS_TEST_RESULT
result=0
service_stopped || result=$?
[ "$result" -eq 7 ]
rm "$work/etc/openpass/service-disabled"
: >"$OPENPASS_TEST_LOG"
service_stopped
[ ! -s "$OPENPASS_TEST_LOG" ]
start_service
grep -Fx 'open openpass' "$OPENPASS_TEST_LOG" >/dev/null
grep -Fx 'param term_timeout 30' "$OPENPASS_TEST_LOG" >/dev/null
printf '%s\n' 'OpenPass service stop tests passed'
