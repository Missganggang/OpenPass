#!/bin/sh
# Download a verified release from the official OpenPass repository.
set -eu
umask 077

fail() { echo "OpenPass: $*" >&2; exit 1; }
[ "$(id -u)" = 0 ] || fail "Run this installer as root on your OpenWrt router."
[ -f /etc/openwrt_release ] || fail "This installer supports OpenWrt only."

case "$(uname -m)" in
	x86_64|amd64) arch=amd64 ;;
	i[3-6]86|x86) arch=386 ;;
	*) fail "Unsupported CPU architecture; only x86_64 and 32-bit x86 are supported." ;;
esac
command -v sha256sum >/dev/null 2>&1 || fail "sha256sum is required (included in standard OpenWrt BusyBox)."

repo=https://github.com/Missganggang/OpenPass
version=${OPENPASS_VERSION:-latest}
if [ "$version" = latest ]; then
	base="$repo/releases/latest/download"
else
	case "$version" in
		v[0-9]*) ;;
		*) fail "OPENPASS_VERSION must be a release tag such as v0.1.4, or latest." ;;
	esac
	case "$version" in *[!A-Za-z0-9._-]*) fail "Invalid release tag." ;; esac
	base="$repo/releases/download/$version"
fi

download() {
	if command -v curl >/dev/null 2>&1; then
		curl --fail --location --retry 2 --connect-timeout 20 --max-time 300 "$1" -o "$2"
	elif command -v wget >/dev/null 2>&1; then
		wget -T 60 -O "$2" "$1"
	else
		fail "Install curl or wget with HTTPS support first."
	fi
}

work=$(mktemp -d /tmp/openpass-download.XXXXXX)
trap 'rm -rf "$work"' 0
trap 'exit 1' 1 2 15
asset="openpass-linux-$arch.tar.gz"
echo "Downloading OpenPass $version for $arch..."
download "$base/$asset" "$work/$asset" || fail "Cannot download $asset. Check Internet access and the release at $repo/releases."
download "$base/SHA256SUMS" "$work/SHA256SUMS" || fail "Cannot download release checksums."

# Check only this architecture from the shared release checksum list.
expected=$(awk -v file="$asset" '$2 == file && length($1) == 64 && $1 !~ /[^0-9a-fA-F]/ { print $1 }' "$work/SHA256SUMS")
[ "${#expected}" -eq 64 ] || fail "Release checksum is missing, duplicated, or malformed."
actual=$(sha256sum "$work/$asset")
actual=${actual%% *}
[ "$actual" = "$expected" ] || fail "SHA256 verification failed. No files have been installed."
tar -xzf "$work/$asset" -C "$work" || fail "Cannot extract release archive."
[ -f "$work/openpass/install.sh" ] || fail "Release archive has no installer."
sh "$work/openpass/install.sh"
