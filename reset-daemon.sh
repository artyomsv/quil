#!/usr/bin/env bash
# Kill the aethel daemon and reset all persisted state
set -euo pipefail

if pkill -f aetheld 2>/dev/null; then
  echo "Daemon killed"
else
  echo "Daemon not running"
fi

rm -rf ~/.aethel/workspace.json ~/.aethel/workspace.json.bak ~/.aethel/buffers/ ~/.aethel/aetheld.pid
echo "State cleaned"
