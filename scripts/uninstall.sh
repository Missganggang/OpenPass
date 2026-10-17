#!/bin/sh
# Remove only OpenPass. Shared sing-box and other proxy services are untouched.
# The LuCI caller runs a private copy and owns the control lock until return.
set -eu
umask 077
fail() { echo "OpenPass: $*" >&2; exit 1; }
# Also support a direct SSH invocation without removing the executing source.
if [ "$0" = /usr/share/openpass/uninstall.sh ]; then
	private_work=$(mktemp -d /tmp/openpass-uninstall.XXXXXX) || fail 'Cannot prepare uninstaller.'
	cp "$0" "$private_work/uninstall.sh" || fail 'Cannot copy uninstaller.'
	OPENPASS_UNINSTALL_WORK="$private_work" exec sh "$private_work/uninstall.sh" "$@"
fi
if [ -n "${OPENPASS_UNINSTALL_WORK:-}" ]; then
	case "$OPENPASS_UNINSTALL_WORK" in
		/tmp/openpass-uninstall.*)
			private_suffix=${OPENPASS_UNINSTALL_WORK#/tmp/openpass-uninstall.}
			case "$private_suffix" in
				""|*[!A-Za-z0-9]*) ;;
				*) trap 'rm -rf "$OPENPASS_UNINSTALL_WORK"' 0 ;;
			esac
			;;
	esac
fi
locked=0
if [ "${1:-}" = --control-locked ]; then locked=1; shift; fi
[ "$#" -le 1 ] || fail 'Expected keep or purge.'
mode=${1:-keep}
case "$mode" in keep|purge) ;; *) fail 'Expected keep or purge.' ;; esac
[ "$(id -u)" = 0 ] || fail 'Run the uninstaller as root.'
[ -f /etc/openwrt_release ] || fail 'This uninstaller supports OpenWrt only.'
if [ "$locked" = 0 ]; then
	mkdir -p /var/lock
	exec 9>/var/lock/openpass-control.lock
	flock -n 9 || fail 'Another OpenPass operation is in progress.'
fi
[ -x /etc/init.d/openpass ] && [ -x /usr/bin/openpassd ] || fail 'OpenPass cleanup components are missing; reinstall before retrying.'
mkdir -p /etc/openpass
printf '%s\n' pending >/etc/openpass/service-disabled
# Try both operations, but never delete programs after any cleanup failure.
cleanup_ok=1
/etc/init.d/openpass disable 9>&- || cleanup_ok=0
/etc/init.d/openpass stop 9>&- || cleanup_ok=0
if /etc/init.d/openpass running 9>&- >/dev/null 2>&1; then cleanup_ok=0; fi
[ "$cleanup_ok" = 1 ] || fail 'Service/rules cleanup failed; all installed files were retained. Retry after resolving the error.'
# stop already runs the post-procd cleanup. Do not repeat a potentially long
# failed stop inside rpcd's command timeout; this extra check is only needed
# after a successful stop (and for firmware with a different init helper).
/usr/bin/openpassd -cleanup 9>&- || fail 'Rules cleanup failed; all installed files were retained.'
command -v ip >/dev/null 2>&1 || fail 'Cannot verify TUN cleanup: ip is unavailable.'
if ip link show dev openpass0 >/dev/null 2>&1; then
	fail 'OpenPass TUN still exists; files were retained. Stop the remaining OpenPass process and retry.'
fi
printf '%s\n' stopped >/etc/openpass/service-disabled

# The daemon has emptied its own rules. Remove its empty declarations without
# restarting the global firewall or disturbing another proxy's routing tables.
command -v nft >/dev/null 2>&1 || fail 'Cannot verify firewall cleanup: nft is unavailable.'
nft list ruleset >/dev/null 2>&1 || fail 'Cannot inspect the firewall; files were retained.'

	# A regular fw4 forward-chain jump must be removed before its destination
	# chain can be deleted. Match only our exact comment and use numeric handles
	# in the fixed fw4/forward chain; never flush another service's rules.
	if nft list chain inet fw4 openpass_l2tp_forward >/dev/null 2>&1; then
		command -v jsonfilter >/dev/null 2>&1 || fail 'Cannot inspect L2TP firewall rules: jsonfilter is unavailable.'
		forward_json=$(nft -j -a list chain inet fw4 forward) || fail 'Cannot inspect the forward chain; files were retained.'
		# jsonfilter exits 1 for an absent match. An already-removed jump is
		# safe; deleting the destination below still rejects any live reference.
		jump_handles=$(printf '%s\n' "$forward_json" | jsonfilter -e '@.nftables[@.rule.comment="openpass-l2tp-forward"].rule.handle' || true)
		for handle in $jump_handles; do
			case "$handle" in ""|*[!0-9]*) fail 'Invalid L2TP firewall handle; files were retained.' ;; esac
			nft delete rule inet fw4 forward handle "$handle" || fail 'Could not remove the L2TP forward rule; files were retained.'
		done
	fi
	for chain in openpass_prerouting openpass_output openpass_forward openpass_dns_nat openpass_dns_output openpass_l2tp_forward openpass_l2tp_nat; do
		if nft list chain inet fw4 "$chain" >/dev/null 2>&1; then
			nft delete chain inet fw4 "$chain" || fail 'Could not remove OpenPass firewall chains; files were retained.'
		fi
	done
	for nft_set in proxy_clients direct_clients blocked_clients; do
		if nft list set inet fw4 "$nft_set" >/dev/null 2>&1; then
			nft delete set inet fw4 "$nft_set" || fail 'Could not remove OpenPass firewall sets; files were retained.'
		fi
	done

rm -f /etc/nftables.d/90-openpass.nft /etc/nftables.d/91-openpass-dynamic.nft \
	/usr/share/nftables.d/ruleset-post/92-openpass-policy.nft \
	/usr/share/nftables.d/chain-pre/forward/30-openpass-l2tp.nft \
	/usr/share/luci/menu.d/luci-app-openpass.json \
	/usr/share/rpcd/acl.d/luci-app-openpass.json \
	/usr/libexec/rpcd/openpass \
	/usr/lib/lua/luci/controller/openpass.lua /usr/lib/lua/luci/model/openpass.lua \
	/usr/lib/lua/luci/view/openpass/service.htm \
	/etc/init.d/openpass /usr/bin/openpassd
rm -rf /usr/share/openpass /www/luci-static/resources/view/openpass \
	/var/etc/openpass /var/run/openpass
rm -f /etc/rc.d/S??openpass /etc/rc.d/K??openpass
if [ "$mode" = purge ]; then
	rm -rf /etc/openpass
else
	# Retain user state and backups, not runtime firewall fragments or stop intent.
	rm -f /etc/openpass/firewall-policy*.nft /etc/openpass/service-disabled
fi
rm -f /tmp/luci-indexcache /tmp/luci-indexcache.*
if [ -d /tmp/luci-modulecache ]; then
	find /tmp/luci-modulecache -type f -exec rm -f {} \;
fi
# Do not restart rpcd here: the pending uninstall response must reach LuCI.
echo "OpenPass uninstalled (configuration: $mode)."
