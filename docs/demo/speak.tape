Output docs/demo/speak.gif
Source docs/demo/common.tape
Type 'mote run speak "The build finished without errors." -o done.wav'
Sleep 500ms
Enter
Wait
Type "ffprobe -v error -show_entries format=duration -of default=nw=1 done.wav"
Enter
Wait
Sleep 4s
