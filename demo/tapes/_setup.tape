# _setup.tape — shared VHS settings + hidden prep for Echo's demo GIFs.
# Each per-command tape does `Source demo/tapes/_setup.tape`, then `Show` (after
# its own `clear`). Height is set per-tape (before Source) since it varies.
#
# SIMULATION mode: the hidden block sources demo/sim/echo-sim.sh so the typed
# command (up / update / deploy / …) resolves to a faithful sim, not the real
# binary. See demo/sim/echo-sim.sh for the fidelity notes.

Set Shell bash
Set FontFamily "JetBrainsMono Nerd Font Mono"
Set FontSize 17
Set Width 1100
Set Padding 22
Set Margin 16
Set BorderRadius 10
Set Theme "TokyoNight"
Set TypingSpeed 55ms
Set PlaybackSpeed 1.0

Hide
Type "source demo/sim/echo-sim.sh" Enter
Type "echo_ps1" Enter
