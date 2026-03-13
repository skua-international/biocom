#!/usr/bin/env bash
set -euo pipefail

UNIT_NAME="biocom-watchdog-guard.service"
UNIT_SRC="$(cd "$(dirname "$0")" && pwd)/${UNIT_NAME}"
UNIT_DST="/etc/systemd/system/${UNIT_NAME}"

if [[ $EUID -ne 0 ]]; then
    echo "error: must run as root" >&2
    exit 1
fi

if [[ ! -f "$UNIT_SRC" ]]; then
    echo "error: ${UNIT_SRC} not found" >&2
    exit 1
fi

cp "$UNIT_SRC" "$UNIT_DST"
systemctl daemon-reload
systemctl enable --now "$UNIT_NAME"

echo "installed and enabled ${UNIT_NAME}"
systemctl status --no-pager "$UNIT_NAME"
