#!/bin/sh
# Back up OpenPass state and generated configuration to a portable tar.gz.
set -eu
umask 077

ETC=${ETC:-/etc}
VAR=${VAR:-/var}
OUT=${1:-"/tmp/openpass-backup-$(date +%Y%m%d-%H%M%S).tar.gz"}
TMP=$(mktemp -d "${TMPDIR:-/tmp}/openpass-backup.XXXXXX")
trap 'rm -rf "$TMP"' EXIT INT TERM

mkdir -p "$TMP/etc/openpass" "$TMP/var/etc/openpass"
found=0
if [ -d "$ETC/openpass" ]; then
	cp -a "$ETC/openpass/." "$TMP/etc/openpass/"
	found=1
fi
if [ -d "$VAR/etc/openpass" ]; then
	cp -a "$VAR/etc/openpass/." "$TMP/var/etc/openpass/"
	found=1
fi

if [ "$found" -eq 0 ]; then
	echo "No OpenPass configuration found under $ETC/openpass or $VAR/etc/openpass" >&2
	exit 1
fi

cat >"$TMP/MANIFEST" <<EOF
OpenPass backup
Created: $(date -u 2>/dev/null || date)
The archive contains /etc/openpass and /var/etc/openpass files when present.
Restore with: sh scripts/restore.sh <this-file>
EOF
mkdir -p "$(dirname "$OUT")"
tar -czf "$OUT" -C "$TMP" .
chmod 0600 "$OUT"
echo "OpenPass backup written to $OUT"
