#!/bin/sh
# Some vendor fw4 scripts end start() with a bare optional include test.
# If that file is absent, an otherwise successful reload returns status 1.
# Accept only that known case, with no logged error and a valid loaded table.
openpass_fw4_vendor_false_negative() {
	[ "$1" -eq 1 ] || return 1
	[ ! -e "$4" ] || return 1
	[ -r "$3" ] && [ -r "$2" ] || return 1
	awk '
		/^[[:space:]]*start\(\)[[:space:]]*\{[[:space:]]*$/ { inside = 1; last = ""; next }
		inside && /^}[[:space:]]*$/ {
			if (last ~ /^[[:space:]]*test -f \/etc\/firewall\.include && sh \/etc\/firewall\.include[[:space:]]*$/)
				found = 1
			inside = 0
		}
		inside && $0 !~ /^[[:space:]]*(#.*)?$/ { last = $0 }
		END { exit !found }
	' "$3" || return 1
	# In particular, never suppress an nft parser or apply error merely because
	# an earlier ruleset already contains the OpenPass chains.
	if grep -Ei 'error|failed|failure|syntax|not found|no such|permission denied|cannot|could not' "$2" >/dev/null; then
		return 1
	fi
	fw4 check >/dev/null 2>&1 || return 1
	for openpass_chain in openpass_prerouting openpass_output openpass_forward openpass_dns_nat openpass_dns_output; do
		nft list chain inet fw4 "$openpass_chain" >/dev/null 2>&1 || return 1
	done
	for openpass_set in proxy_clients direct_clients blocked_clients; do
		nft list set inet fw4 "$openpass_set" >/dev/null 2>&1 || return 1
	done
	return 0
}

# This no-op hook fixes future reloads too, including sing-box auto_redirect.
# It is created only after the exact vendor case above has been verified.
openpass_fw4_create_optional_hook() {
	[ ! -e "$1" ] && [ ! -L "$1" ] || return 1
	(
		set -C
		umask 022
		cat >"$1" <<'SH'
#!/bin/sh
# OpenPass compatibility: this vendor fw4 expects an optional include to exist.
# Keep the hook successful so sing-box auto_redirect can reload firewall4.
exit 0
SH
	) || return 1
	chmod 0644 "$1"
}
