#!/bin/sh
# Restore an archive produced by backup.sh. Existing state is saved beside the
# target as state.json.before-restore when present.
set -eu
umask 077

ETC=${ETC:-/etc}
VAR=${VAR:-/var}
ARCHIVE=${1:-}
if [ -z "$ARCHIVE" ] || [ ! -f "$ARCHIVE" ]; then
	echo "Usage: $0 /path/to/openpass-backup.tar.gz" >&2
	exit 2
fi

TMP=$(mktemp -d "${TMPDIR:-/tmp}/openpass-restore.XXXXXX")
trap 'rm -rf "$TMP"' EXIT INT TERM
tar -xzf "$ARCHIVE" -C "$TMP"

if [ -f "$TMP/etc/openpass/state.json" ]; then
	mkdir -p "$ETC/openpass"
	if [ -f "$ETC/openpass/state.json" ]; then
		cp -a "$ETC/openpass/state.json" "$ETC/openpass/state.json.before-restore"
	fi
	cp -a "$TMP/etc/openpass/." "$ETC/openpass/"
	chmod 0700 "$ETC/openpass"
	chmod 0600 "$ETC/openpass/state.json"
fi
if [ -d "$TMP/var/etc/openpass" ]; then
	mkdir -p "$VAR/etc/openpass"
	cp -a "$TMP/var/etc/openpass/." "$VAR/etc/openpass/"
fi

# Validate JSON when jq is available, but do not make jq a package dependency.
if command -v jq >/dev/null 2>&1 && [ -f "$ETC/openpass/state.json" ]; then
	jq empty "$ETC/openpass/state.json" >/dev/null || {
		echo "Restored state.json is invalid JSON; the previous file is at state.json.before-restore" >&2
		exit 1
	}
fi

if [ -x "$ETC/init.d/openpass" ]; then
	"$ETC/init.d/openpass" restart || true
fi
echo "OpenPass configuration restored from $ARCHIVE"
