#!/bin/sh
# Validate the installed paths and upgrade preservation without touching the
# host's firewall or service manager. Pass a native-architecture release tar.
set -eu
ARCHIVE=${1:?Usage: package-install.sh /path/to/native-release.tar.gz}
work=$(mktemp -d "${TMPDIR:-/tmp}/openpass-install-test.XXXXXX")
trap 'rm -rf "$work"' 0
trap 'exit 1' 1 2 15
tar -xzf "$ARCHIVE" -C "$work"
for policy in "$work/openpass/etc/nftables.d/90-openpass.nft" "$work/openpass/usr/share/nftables.d/ruleset-post/92-openpass-policy.nft" "$work/openpass/usr/share/nftables.d/chain-pre/forward/30-openpass-l2tp.nft"; do
	if LC_ALL=C grep -q "$(printf '\r')" "$policy"; then
		echo "Firewall policy must use LF line endings: $policy" >&2
		exit 1
	fi
done
[ ! -e "$work/openpass/etc/openpass/state.json" ]
DESTDIR="$work/staged" sh "$work/openpass/install.sh"
test -f "$work/staged/www/luci-static/resources/view/openpass/service.js"
test -f "$work/staged/usr/share/luci/menu.d/luci-app-openpass.json"
test -f "$work/staged/usr/share/rpcd/acl.d/luci-app-openpass.json"
test -x "$work/staged/usr/libexec/rpcd/openpass"
include="$work/staged/usr/share/nftables.d/ruleset-post/92-openpass-policy.nft"
test -r "$include"
grep -Fx 'include "/etc/openpass/firewall-policy*.nft"' "$include" >/dev/null
grep -Fx 'include "/var/run/openpass/91-openpass-*.nft"' "$include" >/dev/null
test -r "$work/staged/usr/share/nftables.d/chain-pre/forward/30-openpass-l2tp.nft"
grep -F 'jump openpass_l2tp_forward comment "openpass-l2tp-forward"' "$work/staged/usr/share/nftables.d/chain-pre/forward/30-openpass-l2tp.nft" >/dev/null
test -f "$work/openpass/scripts/firewall-reload.sh"
test -f "$work/openpass/scripts/firewall-settings.sh"
test -f "$work/openpass/scripts/firewall-l2tp.sh"
for helper in maintenance.sh uninstall.sh update.sh; do
	test -x "$work/staged/usr/share/openpass/$helper"
done
grep -F 'https://github.com/Missganggang/OpenPass' "$work/staged/usr/share/openpass/update.sh" >/dev/null
test ! -e "$work/staged/etc/openpass/offloading-backup"
test "$(stat -c %a "$work/staged/etc/openpass/state.json")" = 600
test "$(stat -c %a "$work/staged/etc/openpass")" = 700
grep -q '"default_mode":"direct"' "$work/staged/etc/openpass/state.json"
before=$(sha256sum "$work/staged/etc/openpass/state.json")
touch "$work/staged/etc/openpass/service-disabled"
DESTDIR="$work/staged" sh "$work/openpass/install.sh"
test "$before" = "$(sha256sum "$work/staged/etc/openpass/state.json")"
test -f "$work/staged/etc/openpass/service-disabled"
echo 'PASS: release install includes LuCI service control and persistent fw4 policy, protects permissions, and preserves configuration and disabled state on upgrade.'
