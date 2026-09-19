# Quil shell integration — OSC 7 + OSC 133 for zsh
# Restore ZDOTDIR permanently, source user's .zshrc
if [ -n "${QUIL_ORIG_ZDOTDIR+x}" ]; then
    ZDOTDIR="${QUIL_ORIG_ZDOTDIR}"
else
    ZDOTDIR="${HOME}"
fi
[ -f "${ZDOTDIR}/.zshrc" ] && . "${ZDOTDIR}/.zshrc"

# OSC 7 hooks (chpwd fires on cd)
__quil_osc7() { printf '\e]7;file://%s%s\e\\' "${HOST:-localhost}" "${PWD}" }
(( ${chpwd_functions[(Ie)__quil_osc7]:-0} )) || chpwd_functions+=(__quil_osc7)

# OSC 133: command markers for notification center
# precmd must capture $? immediately before any other function clobbers it
#
# D reports a COMMAND's exit, so it is emitted only when preexec saw one since
# the previous prompt (zsh's preexec fires for user commands only). A prompt
# drawn with nothing run — the first one after startup, a bare Enter — emits
# no D: the daemon reads every D as "the command you sent has finished", and
# a task handed to a fresh shell used to complete on the startup prompt.
__quil_ran=
__quil_precmd() {
    local ec=$?
    if [ -n "$__quil_ran" ]; then
        printf '\e]133;D;%d\e\\' "$ec"
        __quil_ran=
    fi
    printf '\e]133;A\e\\'
}
__quil_preexec() { __quil_ran=1; printf '\e]133;B\e\\'; }
# Insert precmd FIRST (before osc7) so $? is captured before osc7 runs
(( ${precmd_functions[(Ie)__quil_precmd]:-0} )) || precmd_functions=(__quil_precmd $precmd_functions)
(( ${precmd_functions[(Ie)__quil_osc7]:-0} )) || precmd_functions+=(__quil_osc7)
(( ${preexec_functions[(Ie)__quil_preexec]:-0} )) || preexec_functions+=(__quil_preexec)

# Hand-started agent interception (issue #221). See bash-init.sh for the
# reasoning; zsh differs only in the read flag, the subshell variable and the
# function-exists test. zsh needs no version gate — `read -k` is native.
if [ -n "${QUIL_INTERCEPT}" ] && [ -n "${QUIL_INTERCEPT_TOKEN}" ]; then

__quil_esc() {
    local s=${1//\%/%25}
    s=${s//;/%3B}
    s=${s//,/%2C}
    printf '%s' "$s"
}

__quil_intercept() {
    local __qn=$1; shift

    [[ -t 0 && -t 1 ]] || { command "$__qn" "$@"; return; }
    (( ZSH_SUBSHELL > 0 )) && { command "$__qn" "$@"; return; }
    case "$1" in
        -p|--print|-v|--version|-h|--help) command "$__qn" "$@"; return ;;
    esac

    local __qa= __qfirst=1 __qx
    for __qx in "$@"; do
        if (( __qfirst )); then __qa=$(__quil_esc "$__qx"); __qfirst=0
        else __qa="$__qa,$(__quil_esc "$__qx")"; fi
    done

    local __qe= __qv
    for __qv in ${(k)parameters[(I)CLAUDE_*|ANTHROPIC_*|CODEX_*|OPENAI_*|OPENCODE_*]}; do
        __qe="${__qe:+$__qe,}$__qv"
    done

    local __qts=
    zmodload -F zsh/datetime +p:EPOCHREALTIME 2>/dev/null && __qts=$EPOCHREALTIME

    local __qpay="cmd;${QUIL_INTERCEPT_TOKEN};${__qn};$(__quil_esc "$PWD");${__qts};${__qe};${__qa}"
    if (( ${#__qpay} > 2048 )); then
        __qpay="cmd;${QUIL_INTERCEPT_TOKEN};${__qn};;;;!"
    fi

    local __qstty
    __qstty=$(stty -g < /dev/tty 2>/dev/null)
    [ -n "$__qstty" ] && stty -echo < /dev/tty 2>/dev/null

    printf '\e]7770;%s\e\\' "$__qpay" > /dev/tty

    local __qreply=
    read -t 1 -k 8 __qreply < /dev/tty

    [ -n "$__qstty" ] && stty "$__qstty" < /dev/tty 2>/dev/null

    if [ "$__qreply" = "quil:cnv" ]; then
        return 0
    fi
    command "$__qn" "$@"
}

__quil_arm_intercept() {
    local __qn
    for __qn in ${(s:,:)QUIL_INTERCEPT}; do
        [[ "$__qn" =~ ^[A-Za-z0-9._-]+$ ]] || continue
        (( ${+functions[$__qn]} )) && continue
        eval "${__qn}() { __quil_intercept ${__qn} \"\$@\"; }"
    done
}
__quil_arm_intercept
unfunction __quil_arm_intercept

fi
