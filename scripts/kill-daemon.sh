#!/usr/bin/env bash
# Kill the quil daemon
set -euo pipefail

if pkill -f quild 2>/dev/null; then
  echo "Daemon killed"
else
  echo "Daemon not running"
fi
