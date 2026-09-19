# Quil shell integration — OSC 7 + OSC 133 for bash
# Source user's bashrc (--rcfile replaces normal loading)
if [ -f ~/.bashrc ]; then . ~/.bashrc; fi

# Emit OSC 7 with current working directory after every command
__quil_osc7() { printf '\e]7;file://%s%s\e\\' "${HOSTNAME:-localhost}" "$PWD"; }

# OSC 133: command markers for notification center
#
# D reports a COMMAND's exit, so it is emitted only when preexec saw one since
# the previous prompt. A prompt drawn with nothing run — the first one after
# startup, a bare Enter — emits no D: the daemon reads every D as "the command
# you sent has finished", and a task handed to a fresh shell used to complete
# on the startup prompt.
#
# The DEBUG trap fires for every top-level simple command, INCLUDING each piece
# of PROMPT_COMMAND, so preexec only counts a command while the prompt is
# "armed" — set by __quil_arm, the LAST piece of PROMPT_COMMAND, and cleared by
# the first command after it, which is the user's.
__quil_ran=
__quil_armed=
__quil_precmd() {
    local ec=$?
    if [ -n "$__quil_ran" ]; then
        printf '\e]133;D;%d\e\\' "$ec"
        __quil_ran=
    fi
    printf '\e]133;A\e\\'
}
__quil_arm() { __quil_armed=1; }
__quil_preexec() {
    [ -n "$__quil_armed" ] || return 0
    __quil_armed=
    __quil_ran=1
    printf '\e]133;B\e\\'
}

if [[ "${PROMPT_COMMAND}" != *"__quil_osc7"* ]]; then
    PROMPT_COMMAND="__quil_precmd;__quil_osc7${PROMPT_COMMAND:+;$PROMPT_COMMAND};__quil_arm"
fi
trap '__quil_preexec' DEBUG

# Hand-started agent interception (issue #221).
#
# QUIL_INTERCEPT is a comma-separated list of agent binaries the daemon knows
# how to spawn as a typed pane. For each, define a function that shadows the
# PATH lookup, so the daemon learns the argv BEFORE the binary execs and can
# open the pane as that agent instead. Quil's rc is sourced after the user's,
# which is what makes this definition win.
#
# read -N arrived in bash 4.1. On an older bash nothing is defined at all and
# the agent runs exactly as typed — the pre-feature behaviour, not a degraded
# one. macOS ships bash 3.2, so this is the common case there.
if [ -n "${QUIL_INTERCEPT}" ] && [ -n "${QUIL_INTERCEPT_TOKEN}" ] &&
   { [ "${BASH_VERSINFO[0]}" -gt 4 ] ||
     { [ "${BASH_VERSINFO[0]}" -eq 4 ] && [ "${BASH_VERSINFO[1]}" -ge 1 ]; }; }; then

# Percent-encode the three characters the marker's own grammar uses, so a
# directory or argument containing one cannot shift the daemon's field split.
# `;` separates fields and `,` separates argv elements; `%` must go first or
# the encoding is ambiguous. Pure parameter expansion — no fork.
__quil_esc() {
    local s=${1//\%/%25}
    s=${s//;/%3B}
    s=${s//,/%2C}
    printf '%s' "$s"
}

__quil_intercept() {
    local __qn=$1; shift

    # Both streams must be terminals. `$(claude -p …)` has a tty stdin and a
    # captured stdout: a marker written there would land in the captured
    # string and the daemon would never see it.
    [[ -t 0 && -t 1 ]] || { command "$__qn" "$@"; return; }

    # `claude &`, `( claude )` and `$(claude)` all raise BASH_SUBSHELL. A read
    # from the tty in a background job takes SIGTTIN and stops it.
    (( BASH_SUBSHELL > 0 )) && { command "$__qn" "$@"; return; }

    # Not sessions. Short-circuited here rather than round-tripped for a
    # "run" the daemon would always give.
    case "$1" in
        -p|--print|-v|--version|-h|--help) command "$__qn" "$@"; return ;;
    esac

    local __qa= __qfirst=1 __qx
    for __qx in "$@"; do
        if (( __qfirst )); then __qa=$(__quil_esc "$__qx"); __qfirst=0
        else __qa="$__qa,$(__quil_esc "$__qx")"; fi
    done

    # NAMES only, never values: the marker is echoed into the pane's output and
    # persisted in its ghost buffer. The daemon compares the names against its
    # own environment and declines to convert when one is missing.
    local __qe= __qv
    for __qv in ${!CLAUDE_@} ${!ANTHROPIC_@} ${!CODEX_@} ${!OPENAI_@} ${!OPENCODE_@}; do
        __qe="${__qe:+$__qe,}$__qv"
    done

    local __qpay="cmd;${QUIL_INTERCEPT_TOKEN};${__qn};$(__quil_esc "$PWD");${EPOCHREALTIME:-};${__qe};${__qa}"
    # Over the cap the daemon cannot classify, so say so rather than truncate
    # into a different command than the user typed.
    if (( ${#__qpay} > 2048 )); then
        __qpay="cmd;${QUIL_INTERCEPT_TOKEN};${__qn};;;;!"
    fi

    # Echo off BEFORE the marker, not before the read: the line discipline
    # applies echo when bytes ARRIVE, and stty is a fork+exec, so a fast reply
    # would paint the answer on screen.
    local __qstty
    __qstty=$(stty -g < /dev/tty 2>/dev/null)
    [ -n "$__qstty" ] && stty -echo < /dev/tty 2>/dev/null

    printf '\e]7770;%s\e\\' "$__qpay" > /dev/tty

    # Exactly 8 bytes, no terminator. LF is not Enter under ConPTY and a
    # terminated reply arriving late would submit itself as a prompt; a
    # fixed-length read has neither failure.
    local __qreply=
    read -r -N 8 -t 1 __qreply < /dev/tty

    [ -n "$__qstty" ] && stty "$__qstty" < /dev/tty 2>/dev/null

    if [ "$__qreply" = "quil:cnv" ]; then
        return 0
    fi
    command "$__qn" "$@"
}

__quil_arm_intercept() {
    local IFS=,
    local __qn
    for __qn in $QUIL_INTERCEPT; do
        # A name Quil did not vet must never reach eval.
        [[ "$__qn" =~ ^[A-Za-z0-9._-]+$ ]] || continue
        # A user's own wrapper exists to set environment. It keeps winning.
        declare -F "$__qn" > /dev/null 2>&1 && continue
        eval "${__qn}() { __quil_intercept ${__qn} \"\$@\"; }"
    done
}
__quil_arm_intercept
unset -f __quil_arm_intercept

fi
