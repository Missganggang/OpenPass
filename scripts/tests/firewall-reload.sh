#!/bin/sh
set -eu
ROOT=$(CDPATH= cd -- "$(dirname -- "$0")/../.." && pwd)
. "$ROOT/scripts/firewall-reload.sh"
work=$(mktemp -d "${TMPDIR:-/tmp}/openpass-fw4-test.XXXXXX")
trap 'rm -rf "$work"' 0
trap 'exit 1' 1 2 15
mkdir "$work/bin"
cat >"$work/vendor-fw4" <<'SH'
start() {
	nft -f /tmp/firewall.nft
	test -f /etc/firewall.include && sh /etc/firewall.include
}
SH
cat >"$work/bin/fw4" <<'SH'
#!/bin/sh
[ "$*" = check ] && [ "${MOCK_FW4_FAIL:-0}" = 0 ]
SH
cat >"$work/bin/nft" <<'SH'
#!/bin/sh
[ "$*" != "${MOCK_NFT_FAIL:-}" ]
SH
chmod 0755 "$work/bin/fw4" "$work/bin/nft"
PATH="$work/bin:$PATH"
export PATH
: >"$work/reload.log"
verify() { openpass_fw4_vendor_false_negative "$1" "$work/reload.log" "$work/vendor-fw4" "$work/firewall.include"; }
reject() { if verify "$1"; then echo "Unexpected acceptance: $2" >&2; exit 1; fi; }
verify 1
reject 2 'different failure status'
touch "$work/firewall.include"
reject 1 'optional include exists'
rm "$work/firewall.include"
MOCK_FW4_FAIL=1
export MOCK_FW4_FAIL
reject 1 'invalid generated ruleset'
MOCK_FW4_FAIL=0
MOCK_NFT_FAIL='list chain inet fw4 openpass_dns_nat'
export MOCK_NFT_FAIL
reject 1 'missing chain'
MOCK_NFT_FAIL='list chain inet fw4 openpass_forward'
reject 1 'missing forwarding protection chain'
MOCK_NFT_FAIL='list chain inet fw4 openpass_dns_output'
reject 1 'missing router DNS chain'
MOCK_NFT_FAIL='list set inet fw4 blocked_clients'
reject 1 'missing set'
MOCK_NFT_FAIL=''
echo 'Error: Could not process rule' >"$work/reload.log"
reject 1 'nft failure with stale chains present'
: >"$work/reload.log"
echo 'Section ignored because it is disabled' >"$work/reload.log"
verify 1
printf 'start() {\n\tfalse\n}\n' >"$work/vendor-fw4"
reject 1 'unrecognized vendor implementation'
openpass_fw4_create_optional_hook "$work/firewall.include"
sh "$work/firewall.include"
before=$(cat "$work/firewall.include")
if openpass_fw4_create_optional_hook "$work/firewall.include"; then
	echo 'Existing optional hook was overwritten' >&2; exit 1
fi
[ "$before" = "$(cat "$work/firewall.include")" ]
ln -s "$work/missing" "$work/link.include"
if openpass_fw4_create_optional_hook "$work/link.include"; then
	echo 'Dangling symlink was followed' >&2; exit 1
fi
[ ! -e "$work/missing" ]
echo 'PASS: precise vendor detection, genuine failures rejected, no-op compatibility hook created, existing hooks and symlinks preserved.'
