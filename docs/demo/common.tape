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
Env PATH "/tmp/mote-demo/.local/bin:/usr/local/bin:/usr/bin:/bin"
Hide
Type "cd ~/demo 2>/dev/null; clear"
Enter
Show
