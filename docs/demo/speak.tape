Output docs/demo/speak.gif
Set Height 900
Source docs/demo/common.tape
Hide
Type 'mote run speak "The build finished without errors." -o done.wav'
Show
Sleep 600ms
Enter
Wait
Sleep 1s
Hide
Type 'ffprobe -v error -show_entries format=duration -of default=nw=1 done.wav'
Show
Sleep 600ms
Enter
Wait
Sleep 4s
