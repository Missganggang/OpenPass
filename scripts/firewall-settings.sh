#!/bin/sh
# Offloaded forwarding can bypass per-device rules. fw4 automatic includes
# must remain enabled so protection survives firewall reloads and reboots.
openpass_configure_fw4_defaults() {
	uci -q get 'firewall.@defaults[0]' >/dev/null || return 1
	openpass_old_soft=$(uci -q get 'firewall.@defaults[0].flow_offloading' || true)
	openpass_old_hw=$(uci -q get 'firewall.@defaults[0].flow_offloading_hw' || true)
	openpass_old_includes=$(uci -q get 'firewall.@defaults[0].auto_includes' || true)
	[ ! -L "$1" ] || return 1
	if [ ! -e "$1" ]; then
		(
			set -C
			umask 077
			printf '%s\n' '# Original firewall defaults before OpenPass installation; not a shell script.' \
				"flow_offloading=${openpass_old_soft:-<unset>}" \
				"flow_offloading_hw=${openpass_old_hw:-<unset>}" \
				"auto_includes=${openpass_old_includes:-<unset>}" >"$1"
		) || return 1
	fi
	chmod 0600 "$1" || return 1
	openpass_uci_changed=0
	if [ "$openpass_old_soft" != 0 ]; then
		uci set 'firewall.@defaults[0].flow_offloading=0' || return 1
		openpass_uci_changed=1
	fi
	if [ "$openpass_old_hw" != 0 ]; then
		uci set 'firewall.@defaults[0].flow_offloading_hw=0' || return 1
		openpass_uci_changed=1
	fi
	if [ "$openpass_old_includes" != 1 ]; then
		uci set 'firewall.@defaults[0].auto_includes=1' || return 1
		openpass_uci_changed=1
	fi
	if [ "$openpass_uci_changed" -eq 1 ]; then
		uci commit firewall || return 1
	fi
	return 0
}
