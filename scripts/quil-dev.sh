#!/usr/bin/env bash
# Launch Quil in dev mode (uses .quil/ in project root)
exec "$(dirname "$0")/quil" --dev "$@"
