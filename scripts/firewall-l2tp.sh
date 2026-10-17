#!/bin/sh
# Add the empty L2TP declarations when upgrading a stopped installation. The
# regular installer reloads fw4 only while OpenPass is active; a stopped
# installation must not reload another proxy's firewall merely to gain these
# new declarations. This helper never flushes chains, changes policies, or
# starts a service.
openpass_prepare_l2tp_firewall() {
	command -v nft >/dev/null 2>&1 || return 1
	command -v jsonfilter >/dev/null 2>&1 || return 1
	# If firewall4 is itself stopped, its next start loads the installed include
	# files. Do not construct a partial fw4 table while the firewall is absent.
	if ! nft list table inet fw4 >/dev/null 2>&1; then
		# Distinguish an absent table from a failure to communicate with nft.
		nft list ruleset >/dev/null 2>&1 || return 1
		return 0
	fi
	openpass_l2tp_forward_json=$(nft -j -a list chain inet fw4 forward) || return 1
	openpass_l2tp_jump_handles=$(printf '%s\n' "$openpass_l2tp_forward_json" | jsonfilter -e '@.nftables[@.rule.comment="openpass-l2tp-forward"].rule.handle' || true)
	# nft "add chain" is idempotent for an existing declaration and preserves
	# all rules. Install the declarations and any missing jump atomically.
	{
		printf '%s\n' 'add chain inet fw4 openpass_l2tp_forward'
		printf '%s\n' 'add chain inet fw4 openpass_l2tp_nat { type nat hook postrouting priority srcnat + 1; policy accept; }'
		if [ -z "$openpass_l2tp_jump_handles" ]; then
			printf '%s\n' 'insert rule inet fw4 forward jump openpass_l2tp_forward comment "openpass-l2tp-forward"'
		fi
	} | nft -f -
}
