# Community Plugin Registry

| Field | Value |
|-------|-------|
| Priority | 6 |
| Effort | Medium |
| Impact | High |
| Status | Proposed |
| Depends on | — |

## Problem

Aethel ships with 4 built-in plugins (terminal, claude-code, ssh, stripe). Users who want to add support for other tools must write TOML plugin files manually. There's no way to discover or share plugins. Every user reinvents the same configurations.

## Proposed Solution

Create a simple plugin sharing mechanism backed by a GitHub repository (`aethel-plugins`) acting as a registry. Each plugin is a `.toml` file. `aethel plugin install <name>` fetches it to `~/.aethel/plugins/`.

```bash
aethel plugin install aider       # pulls community TOML plugin
aethel plugin install k9s
aethel plugin install lazygit
aethel plugin search docker
```

### High-Value Plugins to Ship or Solicit

| Plugin | Resume Strategy | Value |
|--------|----------------|-------|
| **Aider** | `session_scrape` | 2nd most popular AI coding tool |
| **Cline/Continue** | `rerun` | VS Code AI tools used from CLI |
| **lazygit** | `cwd_only` | Git UI loved by terminal users |
| **k9s** | `rerun` | Kubernetes dashboard |
| **Docker Compose** | `rerun` | `docker compose up` with auto-restart |
| **ngrok/localtunnel** | `rerun` | Tunnel with auto-restart |
| **npm/cargo/go watch** | `rerun` | Build watchers |
| **pgcli/mongosh** | `rerun` | Database CLIs |

**Network effect:** Every plugin makes Aethel useful to a new audience. Plugin authors become advocates.

## User Experience

### Installing Plugins

```bash
# Install by name
aethel plugin install aider

# Search available plugins
aethel plugin search database
# Results:
#   pgcli       - PostgreSQL CLI with auto-complete
#   mongosh     - MongoDB Shell
#   redis-cli   - Redis CLI

# List installed plugins
aethel plugin list

# Update all plugins
aethel plugin update
```

### Creating and Sharing Plugins

```bash
# Create a plugin from template
aethel plugin init my-tool

# Test locally
# (edit ~/.aethel/plugins/my-tool.toml, use Ctrl+N to create pane)

# Submit to registry
# (PR to aethel-plugins repo on GitHub)
```

## Technical Approach

### 1. Registry Structure

GitHub repo `artyomsv/aethel-plugins`:

```
aethel-plugins/
├── registry.json          # index: name → metadata
├── plugins/
│   ├── aider.toml
│   ├── k9s.toml
│   ├── lazygit.toml
│   ├── docker-compose.toml
│   ├── ngrok.toml
│   └── pgcli.toml
└── README.md
```

`registry.json`:
```json
{
  "plugins": [
    {
      "name": "aider",
      "description": "AI pair programmer",
      "version": "1.0.0",
      "author": "community",
      "tags": ["ai", "coding"]
    }
  ]
}
```

### 2. CLI Commands

| Command | Action |
|---------|--------|
| `aethel plugin install <name>` | Download TOML from registry to `~/.aethel/plugins/` |
| `aethel plugin search <query>` | Search registry.json by name/tags/description |
| `aethel plugin list` | Show installed plugins (built-in + community) |
| `aethel plugin update` | Re-fetch all community plugins |
| `aethel plugin remove <name>` | Delete community plugin |
| `aethel plugin init <name>` | Create plugin template |

### 3. Implementation

- Fetch `registry.json` from GitHub raw URL (cached locally)
- Download individual TOML files on install
- No authentication required (public repo)
- Version tracking in `~/.aethel/plugins/.registry-cache.json`

### 4. Files

| File | Change |
|------|--------|
| `cmd/aethel/plugin.go` | New — plugin subcommand (install, search, list, update, remove) |
| `internal/plugin/registry_remote.go` | New — GitHub registry client |
| `internal/plugin/cache.go` | New — local cache management |

## Success Criteria

- [ ] `aethel plugin install aider` downloads and installs the plugin
- [ ] `aethel plugin search ai` returns matching plugins
- [ ] Installed plugins appear in `Ctrl+N` pane creation dialog
- [ ] At least 5 community plugins published in registry
- [ ] Plugin submission via PR is documented

## Open Questions

- Should plugins be versioned? (semver in registry.json)
- Plugin validation before publishing? (CI checks on PR)
- Allow plugins to declare binary dependencies? (`requires = ["aider"]`)
- CDN caching for plugin downloads vs direct GitHub raw URLs?
