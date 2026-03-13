#!/usr/bin/env bash
# Kill the aethel daemon
set -euo pipefail

if pkill -f aetheld 2>/dev/null; then
  echo "Daemon killed"
else
  echo "Daemon not running"
fi
