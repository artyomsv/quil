# Quil shell integration — zsh environment bootstrap
# Save our ZDOTDIR, restore original so user's .zshenv is found
QUIL_ZDOTDIR="${ZDOTDIR}"
if [ -n "${QUIL_ORIG_ZDOTDIR+x}" ]; then
    ZDOTDIR="${QUIL_ORIG_ZDOTDIR}"
else
    ZDOTDIR="${HOME}"
fi
[ -f "${ZDOTDIR}/.zshenv" ] && . "${ZDOTDIR}/.zshenv"
ZDOTDIR="${QUIL_ZDOTDIR}"
