Output docs/demo/install.gif
Source docs/demo/common.tape
Set Height 1300
Hide
Type 'curl -fsSL https://raw.githubusercontent.com/JGalego/Mote/main/install/install.sh | sh'
Show
Sleep 600ms
Enter
# profile
Wait+Screen /choice \[1\]:/
Sleep 1.5s
Enter
Sleep 1s
# editor
Wait+Screen /choice \[1\]:/
Sleep 1.5s
Enter
Sleep 1s
# data directory
Wait+Screen /and runtime/
Sleep 1.5s
Enter
Sleep 1s
# automatic downloads
Wait+Screen /\[y\/N\]:/
Sleep 1.5s
Enter
Sleep 1s
# download the text model now
Wait+Screen /\[Y\/n\]:/
Sleep 1.5s
Enter
Sleep 1s
Wait
Sleep 1s
Hide
Type 'mote doctor'
Show
Sleep 600ms
Enter
Wait
Sleep 5s
