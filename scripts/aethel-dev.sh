#!/usr/bin/env bash
# Launch Aethel in dev mode (uses .aethel/ in project root)
exec "$(dirname "$0")/aethel" --dev "$@"
