# Shared settings. Recordings run against a throwaway $HOME; see README.md.
# Each tape sets its Height before sourcing this file; crop.sh trims the
# unused space afterwards.
Set Shell bash
Set FontSize 22
Set Width 1000
Set Padding 18
Set Framerate 15
Set WaitTimeout 600s
Set Theme "Catppuccin Mocha"
Env HOME "/tmp/mote-demo"
# XDG variables win over HOME, and some desktops (a snap-packaged editor,
# for one) set them; pinned here, the recording cannot reach real data.
Env XDG_DATA_HOME "/tmp/mote-demo/.local/share"
Env XDG_CONFIG_HOME "/tmp/mote-demo/.config"
Env PATH "/tmp/mote-demo/.local/bin:/usr/local/bin:/usr/bin:/bin"
Hide
Type "cd ~/demo 2>/dev/null; clear"
Enter
Show
