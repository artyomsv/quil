# Aethel shell integration — OSC 7 + OSC 133 for zsh
# Restore ZDOTDIR permanently, source user's .zshrc
if [ -n "${AETHEL_ORIG_ZDOTDIR+x}" ]; then
    ZDOTDIR="${AETHEL_ORIG_ZDOTDIR}"
else
    ZDOTDIR="${HOME}"
fi
[ -f "${ZDOTDIR}/.zshrc" ] && . "${ZDOTDIR}/.zshrc"

# OSC 7 hooks (chpwd fires on cd)
__aethel_osc7() { printf '\e]7;file://%s%s\e\\' "${HOST:-localhost}" "${PWD}" }
(( ! ${chpwd_functions[(Ie)__aethel_osc7]} )) && chpwd_functions+=(__aethel_osc7)

# OSC 133: command markers for notification center
# precmd must capture $? immediately before any other function clobbers it
__aethel_precmd() {
    local ec=$?
    printf '\e]133;D;%d\e\\' "$ec"
    printf '\e]133;A\e\\'
}
__aethel_preexec() { printf '\e]133;B\e\\'; }
# Insert precmd FIRST (before osc7) so $? is captured before osc7 runs
(( ! ${precmd_functions[(Ie)__aethel_precmd]} )) && precmd_functions=(__aethel_precmd $precmd_functions)
(( ! ${precmd_functions[(Ie)__aethel_osc7]} )) && precmd_functions+=(__aethel_osc7)
(( ! ${preexec_functions[(Ie)__aethel_preexec]} )) && preexec_functions+=(__aethel_preexec)
