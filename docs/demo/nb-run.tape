Output docs/demo/nb-run.gif
Set Height 1000
Source docs/demo/common.tape
Hide
Type "cp -f standup.src.md standup.mote.md; clear"
Enter
Show
Type "cat standup.mote.md"
Sleep 400ms
Enter
Sleep 2.5s
Type "mote nb run standup.mote.md"
Sleep 400ms
Enter
Wait
Sleep 1.5s
Type "mote nb run standup.mote.md   # again: nothing changed"
Sleep 400ms
Enter
Wait
Sleep 1.5s
Type "clear; cat standup.mote.md"
Sleep 400ms
Enter
Sleep 5s
