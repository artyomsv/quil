#!/usr/bin/env bash
# Kill the quil daemon and reset all persisted state
set -euo pipefail

if pkill -f quild 2>/dev/null; then
  echo "Daemon killed"
else
  echo "Daemon not running"
fi

rm -rf ~/.quil/workspace.json ~/.quil/workspace.json.bak ~/.quil/buffers/ ~/.quil/quild.pid
echo "State cleaned"
