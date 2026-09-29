#!/bin/sh
# Install a release archive or a built source checkout on OpenWrt.
set -eu
umask 022

ROOT=$(CDPATH= cd -- "$(dirname -- "$0")/.." && pwd)
# DESTDIR stages files without installing dependencies or operating services.
DESTDIR=${DESTDIR:-}
case "$DESTDIR" in ""|/*) ;; *) echo "DESTDIR must be an absolute path" >&2; exit 1 ;; esac
PREFIX="$DESTDIR/usr"
ETC="$DESTDIR/etc"
WWW="$DESTDIR/www"
fail() { echo "OpenPass: $*" >&2; exit 1; }

machine=$(uname -m)
case "$machine" in
	x86_64|amd64) arch=amd64 ;;
	i[3-6]86|x86) arch=386 ;;
	*) fail "Unsupported CPU $machine; use an x86 OpenWrt system." ;;
esac
if [ -f "$ROOT/ARCH" ]; then
	package_arch=$(cat "$ROOT/ARCH")
	[ "$package_arch" = "$arch" ] || fail "This package is for $package_arch, but this router is $arch. Download the correct package."
fi
if [ -f "$ROOT/usr/bin/openpassd" ]; then
	BINARY="$ROOT/usr/bin/openpassd"
	WEB="$ROOT/usr/share/openpass/web"
	INIT="$ROOT/etc/init.d/openpass"
	NFT="$ROOT/etc/nftables.d/90-openpass.nft"
	POLICY_INCLUDE="$ROOT/usr/share/nftables.d/ruleset-post/92-openpass-policy.nft"
else
	BINARY="$ROOT/dist/openpassd-linux-$arch"
	WEB="$ROOT/web"
	INIT="$ROOT/openwrt/openpass.init"
	NFT="$ROOT/openwrt/90-openpass.nft"
	POLICY_INCLUDE="$ROOT/openwrt/92-openpass-policy.nft"
fi
for required in "$BINARY" "$WEB/index.html" "$WEB/app.js" "$WEB/styles.css" "$INIT" "$NFT" "$POLICY_INCLUDE" "$ROOT/scripts/firewall-reload.sh" "$ROOT/scripts/firewall-settings.sh" "$ROOT/luci-app-openpass/htdocs/luci-static/resources/view/openpass/links.js"; do
	[ -f "$required" ] || fail "Required package file is missing: $required"
done

if [ -z "$DESTDIR" ]; then
	[ "$(id -u)" = 0 ] || fail "Run this installer as root."
	[ -f /etc/openwrt_release ] || fail "This installer supports OpenWrt only."
	chmod 0755 "$BINARY"
	"$BINARY" -version >/dev/null || fail "The packaged binary cannot run on this system."
	dependencies=""
	command -v sing-box >/dev/null 2>&1 || dependencies="$dependencies sing-box"
	command -v fw4 >/dev/null 2>&1 || dependencies="$dependencies firewall4"
	command -v nft >/dev/null 2>&1 || dependencies="$dependencies nftables-json"
	[ -c /dev/net/tun ] || dependencies="$dependencies kmod-tun"
	[ -s /etc/ssl/certs/ca-certificates.crt ] || dependencies="$dependencies ca-bundle"
	[ -f /www/luci-static/resources/luci.js ] || dependencies="$dependencies luci"
	if [ -n "$dependencies" ]; then
		echo "Installing dependencies:$dependencies"
		if command -v opkg >/dev/null 2>&1; then
			opkg update || fail "opkg update failed; check router Internet access and software feeds."
			# Intentional splitting: all package names above are fixed.
			opkg install $dependencies || fail "Dependency installation failed. Check firmware feeds and matching kernel modules."
		elif command -v apk >/dev/null 2>&1; then
			apk update || fail "apk update failed; check router Internet access and software feeds."
			apk add $dependencies || fail "Dependency installation failed. Check firmware feeds and matching kernel modules."
		else
			fail "Neither opkg nor apk is available to install dependencies."
		fi
	fi
	command -v modprobe >/dev/null 2>&1 && modprobe tun 2>/dev/null || true
	[ -c /dev/net/tun ] || fail "TUN is unavailable. Install kmod-tun matching this router's kernel."
	singbox_version=$(sing-box version | awk '/sing-box version/ { print $3; exit }')
	case "$singbox_version" in
		1.*) minor=${singbox_version#1.}; minor=${minor%%.*}; minor=${minor%%-*}
			case "$minor" in ""|*[!0-9]*) fail "Cannot determine sing-box version: $singbox_version" ;; esac
			[ "$minor" -ge 12 ] || fail "sing-box 1.12 or newer is required; installed: $singbox_version. Update sing-box or firmware first." ;;
		*) fail "Unsupported sing-box version: $singbox_version (validated with 1.14)." ;;
	esac
fi

mkdir -p "$PREFIX/bin" "$PREFIX/share/openpass/web" "$PREFIX/share/nftables.d/ruleset-post" "$ETC/openpass" "$ETC/init.d" "$ETC/nftables.d" "$WWW"
# A rename on the same filesystem avoids ETXTBSY for a running executable.
binary_tmp="$PREFIX/bin/.openpassd-install.$$"
trap 'rm -f "$binary_tmp"' 0
trap 'exit 1' 1 2 15
cp "$BINARY" "$binary_tmp"
chmod 0755 "$binary_tmp"
mv -f "$binary_tmp" "$PREFIX/bin/openpassd"
cp "$WEB/index.html" "$WEB/app.js" "$WEB/styles.css" "$PREFIX/share/openpass/web/"
chmod 0644 "$PREFIX/share/openpass/web/"*
cp "$ROOT/scripts/backup.sh" "$ROOT/scripts/restore.sh" "$PREFIX/share/openpass/"
chmod 0755 "$PREFIX/share/openpass/backup.sh" "$PREFIX/share/openpass/restore.sh"
cp "$INIT" "$ETC/init.d/openpass"
chmod 0755 "$ETC/init.d/openpass"
cp "$NFT" "$ETC/nftables.d/90-openpass.nft"
chmod 0644 "$ETC/nftables.d/90-openpass.nft"
cp "$POLICY_INCLUDE" "$PREFIX/share/nftables.d/ruleset-post/92-openpass-policy.nft"
chmod 0644 "$PREFIX/share/nftables.d/ruleset-post/92-openpass-policy.nft"

# LuCI needs both the root overlay and the htdocs JavaScript view.
cp -R "$ROOT/luci-app-openpass/root/." "$DESTDIR/"
cp -R "$ROOT/luci-app-openpass/htdocs/." "$WWW/"
chmod 0644 "$WWW/luci-static/resources/view/openpass/links.js"
chmod 0644 "$PREFIX/share/luci/menu.d/luci-app-openpass.json"
# Remove only known files from the old Lua implementation.
rm -f "$PREFIX/lib/lua/luci/controller/openpass.lua" "$PREFIX/lib/lua/luci/model/openpass.lua" "$PREFIX/lib/lua/luci/view/openpass/links.htm" "$PREFIX/share/rpcd/acl.d/luci-app-openpass.json"

if [ ! -f "$ETC/openpass/state.json" ]; then
	cat >"$ETC/openpass/state.json" <<'JSON'
{"settings":{"enabled":false,"kill_switch":true,"default_mode":"direct","default_dns":"aliyun","url_test_address":"https://www.google.com/generate_204","url_test_region":"overseas","web_port":8787,"auto_apply":true,"self_service_enabled":true,"hide_ap":true,"force_doh":true,"proxy_dns":true,"dns_fail_closed":true},"devices":[],"nodes":[]}
JSON
fi
chmod 0700 "$ETC/openpass"
chmod 0600 "$ETC/openpass/state.json"

if [ -z "$DESTDIR" ]; then
	. "$ROOT/scripts/firewall-settings.sh"
	openpass_configure_fw4_defaults /etc/openpass/offloading-backup || fail "Could not preserve and configure firewall offloading/automatic includes."
	# Move runtime commands out of the fw4 include directory used by old builds.
	rm -f /etc/nftables.d/91-openpass-dynamic.nft
	fw4 check || fail "firewall4 configuration validation failed; OpenPass has not been restarted."
	. "$ROOT/scripts/firewall-reload.sh"
	reload_log=$(mktemp /tmp/openpass-firewall-reload.XXXXXX)
	if /etc/init.d/firewall reload >"$reload_log" 2>&1; then
		cat "$reload_log"
		rm -f "$reload_log"
	else
		reload_status=$?
		cat "$reload_log" >&2
		if openpass_fw4_vendor_false_negative "$reload_status" "$reload_log" /sbin/fw4 /etc/firewall.include; then
			echo "Verified vendor fw4 optional-include issue; creating an empty compatibility hook for future reloads."
			openpass_fw4_create_optional_hook /etc/firewall.include || fail "Could not create the optional firewall hook without overwriting an existing file."
			# sing-box TUN auto_redirect also reloads fw4. Merely ignoring the
			# first false status would still make sing-box fail at startup.
			if /etc/init.d/firewall reload >"$reload_log" 2>&1; then
				cat "$reload_log"
				rm -f "$reload_log"
			else
				reload_status=$?
				cat "$reload_log" >&2
				fail "Firewall reload still failed after creating the compatibility hook (status $reload_status). Details: $reload_log"
			fi
		else
			fail "Firewall reload failed (status $reload_status). Details: $reload_log"
		fi
	fi
	/etc/init.d/openpass enable
	/etc/init.d/openpass restart || fail "OpenPass could not be started. Inspect logread -e openpass."
	# The two links require no RPC ACL; rpcd sessions can remain connected.
	rm -f /tmp/luci-indexcache /tmp/luci-indexcache.*
	if [ -d /tmp/luci-modulecache ]; then
		find /tmp/luci-modulecache -type f -exec rm -f {} \;
	fi
	echo "OpenPass installed. LuCI: Services -> OpenPass. Reload LuCI if already open."
	echo "Management: http://<router-lan-ip>:8787/  Device self-service: http://<router-lan-ip>:8787/choose"
	echo "Existing nodes, device bindings, and the global protection setting have been preserved."
else
	echo "OpenPass files staged in $DESTDIR"
fi
