# Image recipe for Quil sandbox panes. Build it with scripts/sandbox-image.sh.
#
# Quil PUBLISHES no image and this file is not built by CI. It is a recipe you
# build yourself, on your own machine, into your own local image — the same
# thing Anthropic's dev-container documentation offers, and for the same
# reason: a vendor that ships Claude Code preinstalled inside its own image is
# "preinstalling or running Claude Code in your products or services" under the
# Commercial Terms, while a user-built image means Quil preinstalls nothing.
#
# There is deliberately no `docker pull` for this. Do not add one: the name
# `anthropics/claude-code` on Docker Hub is NOT Anthropic's — it is a security
# researcher's honeypot, published precisely to catch tools and users who
# assume an official-looking namespace is official. Any default that points at
# a registry name nobody in this repository controls is that same mistake.

ARG BASE_IMAGE=node:22-bookworm-slim
FROM ${BASE_IMAGE}

# CLAUDE_CODE_VERSION pins what npm installs. scripts/sandbox-image.sh always
# passes an EXACT version — it resolves "latest" to a number first — because
# the build cache keys a RUN line on its text: with the literal "latest" a
# rebuild reuses the old layer and the agent never updates, while the build
# still reports success. Pin an exact version for a reproducible team image,
# which is what Anthropic's own dev-container guidance recommends.
ARG CLAUDE_CODE_VERSION=latest
ARG CODEX_VERSION=latest
ARG OPENCODE_VERSION=latest

# AGENT_USER must not be root. Claude Code REFUSES to start with
# --dangerously-skip-permissions as root on Linux and macOS, and running
# unattended behind an isolation boundary is the whole point of a sandbox pane.
ARG AGENT_USER=agent
ARG AGENT_UID=1001

# git is required, not optional: a sandbox pane's checkout is a linked git
# worktree and the pane's own object store is wired up with git commands.
# ripgrep and less are what Claude Code shells out to; without them its search
# and paging degrade silently rather than failing loudly.
RUN apt-get update \
 && apt-get install -y --no-install-recommends \
      git \
      ca-certificates \
      less \
      ripgrep \
 && rm -rf /var/lib/apt/lists/*

RUN npm install -g "@anthropic-ai/claude-code@${CLAUDE_CODE_VERSION}"

# AGENTS names the extra agents to install beside Claude Code, space-separated:
# "codex", "opencode", or both. Empty by default — every extra is another npm
# package in the image and another vendor's release cadence to track, and the
# pane type you pick is the one that has to be present, not all of them.
#
# Installed in ONE layer with a shell loop rather than as separate RUNs, so an
# image built without them carries no empty layers and adding one does not
# reorder the cache for the others.
ARG AGENTS=""
RUN set -eu; \
    for a in ${AGENTS}; do \
      case "$a" in \
        codex)    npm install -g "@openai/codex@${CODEX_VERSION}" ;; \
        opencode) npm install -g "opencode-ai@${OPENCODE_VERSION}" ;; \
        *) echo "unknown agent: $a" >&2; exit 1 ;; \
      esac; \
    done

# A plain home directory the agent owns. Quil mounts the pane's own Claude
# config directory over /quil/claude and points CLAUDE_CONFIG_DIR at it, so
# nothing here holds credentials or transcripts between runs.
RUN useradd --create-home --uid "${AGENT_UID}" --shell /bin/bash "${AGENT_USER}"

USER ${AGENT_USER}

# Claude Code's self-update cannot work here: the package is installed for
# root, and every pane starts a fresh container from this image, so an update
# made inside one is gone at the next start. Updating means rebuilding the
# image with scripts/sandbox-image.sh.
ENV DISABLE_AUTOUPDATER=1

# Quil overrides the working directory per pane (-w). This is only a sane
# default for someone running the image by hand to check it.
WORKDIR /repo

# No ENTRYPOINT. Quil supplies the command, and an entrypoint here would sit
# between docker and the agent process, breaking the pane's TTY handling.
CMD ["bash"]
