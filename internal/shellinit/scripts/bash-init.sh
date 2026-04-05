# Aethel shell integration — OSC 7 + OSC 133 for bash
# Source user's bashrc (--rcfile replaces normal loading)
if [ -f ~/.bashrc ]; then . ~/.bashrc; fi

# Emit OSC 7 with current working directory after every command
__aethel_osc7() { printf '\e]7;file://%s%s\e\\' "${HOSTNAME:-localhost}" "$PWD"; }

# OSC 133: command markers for notification center
__aethel_precmd() {
    local ec=$?
    printf '\e]133;D;%d\e\\' "$ec"
    printf '\e]133;A\e\\'
}
__aethel_preexec() { printf '\e]133;B\e\\'; }

if [[ "${PROMPT_COMMAND}" != *"__aethel_osc7"* ]]; then
    PROMPT_COMMAND="__aethel_precmd;__aethel_osc7${PROMPT_COMMAND:+;$PROMPT_COMMAND}"
fi
trap '__aethel_preexec' DEBUG
