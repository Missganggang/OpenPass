#!/bin/sh
# Build a static, self-contained OpenWrt release archive.
set -eu
ROOT=$(CDPATH= cd -- "$(dirname -- "$0")/.." && pwd)
VERSION=${VERSION:-0.1.8}
ARCH=${ARCH:-amd64}
GO=${GO:-go}
OUT_DIR=${OUT_DIR:-$ROOT/dist}
case "$ARCH" in
	amd64|x86_64) ARCH=amd64 ;;
	386|i386|i686) ARCH=386 ;;
	*) echo "ARCH must be amd64 or 386 (got $ARCH)" >&2; exit 2 ;;
esac
case "$VERSION" in ""|*[!0-9A-Za-z._-]*) echo "Invalid VERSION" >&2; exit 2 ;; esac
command -v "$GO" >/dev/null 2>&1 || { echo "Go compiler '$GO' was not found" >&2; exit 1; }
mkdir -p "$OUT_DIR"
OUT_DIR=$(CDPATH= cd -- "$OUT_DIR" && pwd)
work=$(mktemp -d "${TMPDIR:-/tmp}/openpass-package.XXXXXX")
trap 'rm -rf "$work"' 0
trap 'exit 1' 1 2 15
PKGROOT="$work/openpass"
mkdir -p "$PKGROOT/usr/bin" "$PKGROOT/usr/share/openpass/web" "$PKGROOT/usr/share/nftables.d/ruleset-post" "$PKGROOT/etc/init.d" "$PKGROOT/etc/nftables.d" "$PKGROOT/luci-app-openpass" "$PKGROOT/scripts"
echo "Building OpenPass $VERSION for linux/$ARCH"
cd "$ROOT"
GOOS=linux GOARCH="$ARCH" CGO_ENABLED=0 "$GO" build -trimpath -ldflags "-s -w -X main.version=$VERSION" -o "$PKGROOT/usr/bin/openpassd" ./cmd/openpass
cp "$ROOT/web/index.html" "$ROOT/web/app.js" "$ROOT/web/styles.css" "$PKGROOT/usr/share/openpass/web/"
cp "$ROOT/openwrt/openpass.init" "$PKGROOT/etc/init.d/openpass"
cp "$ROOT/openwrt/90-openpass.nft" "$PKGROOT/etc/nftables.d/90-openpass.nft"
cp "$ROOT/openwrt/92-openpass-policy.nft" "$PKGROOT/usr/share/nftables.d/ruleset-post/92-openpass-policy.nft"
cp -R "$ROOT/luci-app-openpass/." "$PKGROOT/luci-app-openpass/"
[ -f "$PKGROOT/luci-app-openpass/htdocs/luci-static/resources/view/openpass/links.js" ] || { echo "LuCI JavaScript view is missing" >&2; exit 1; }
cp "$ROOT/scripts/install.sh" "$ROOT/scripts/firewall-reload.sh" "$ROOT/scripts/firewall-settings.sh" "$ROOT/scripts/backup.sh" "$ROOT/scripts/restore.sh" "$PKGROOT/scripts/"
cat >"$PKGROOT/install.sh" <<'SH'
#!/bin/sh
exec sh "$(CDPATH= cd -- "$(dirname -- "$0")" && pwd)/scripts/install.sh" "$@"
SH
printf '%s\n' "$ARCH" >"$PKGROOT/ARCH"
printf '%s\n' "$VERSION" >"$PKGROOT/VERSION"
cp "$ROOT/README.md" "$PKGROOT/README.md"
# Normalize modes when archives are produced from Windows checkouts.
find "$PKGROOT" -type d -exec chmod 0755 {} \;
find "$PKGROOT" -type f -exec chmod 0644 {} \;
chmod 0755 "$PKGROOT/usr/bin/openpassd" "$PKGROOT/etc/init.d/openpass" "$PKGROOT/install.sh" "$PKGROOT/scripts/"*.sh
OUT="$OUT_DIR/openpass-linux-$ARCH.tar.gz"
tar -czf "$OUT" -C "$work" openpass
echo "Created $OUT"
