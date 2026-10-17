#!/bin/sh
# Detached update worker. rpcd gives this private copy its locked fd 9.
set -eu
umask 077
. /usr/share/libubox/jshn.sh

WORK=$(CDPATH= cd -- "$(dirname -- "$0")" && pwd)
STATUS=/tmp/openpass-maintenance/status.json
VERSION=${1:-}
case "$VERSION" in *[!v0-9.]*) exit 1 ;; esac
printf '%s\n' "$VERSION" | grep -Eq '^v[0-9]+\.[0-9]+\.[0-9]+$' || exit 1
flock -n 9 || exit 1
trap '' HUP

write_status() {
	json_init
	json_add_string state "$1"
	json_add_string action update
	json_add_string message "$2"
	json_add_string version "$VERSION"
	json_dump >"$STATUS.$$"
	mv -f "$STATUS.$$" "$STATUS"
}

finish() {
	result=$?
	trap - 0
	if [ "$result" != 0 ]; then
		write_status error '更新失败，请查看 /tmp/openpass-maintenance/update.log。已有配置会保留。' || :
	fi
	# ready is kept until the RPC has returned, even for immediate failures.
	sleep 5
	rm -rf "$WORK"
	exit "$result"
}
trap finish 0
trap 'exit 1' INT TERM
write_status running '正在下载并安装新版本，请勿断电。'
touch "$WORK/ready"
# No descendant, including the restarted daemon/rpcd, may inherit our lock.
OPENPASS_VERSION="$VERSION" sh "$WORK/install.sh" 9>&-
installed_version=$(/usr/bin/openpassd -version 9>&-)
[ "$installed_version" = "${VERSION#v}" ] || exit 1
write_status success '更新完成，请刷新 LuCI 页面。'
