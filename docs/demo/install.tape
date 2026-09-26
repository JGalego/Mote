Output docs/demo/install.gif
Source docs/demo/common.tape
Set Height 720
Type "curl -fsSL https://raw.githubusercontent.com/JGalego/Mote/main/install/install.sh | sh"
Sleep 500ms
Enter
# profile
Wait /choice \[1\]:/
Sleep 2s
Enter
Sleep 1s
# editor
Wait /choice \[1\]:/
Sleep 1s
Enter
Sleep 1s
# data directory
Wait /and runtime/
Sleep 1s
Enter
Sleep 1s
# automatic downloads
Wait /\[y\/N\]:/
Sleep 1s
Enter
Sleep 1s
# download the text model now
Wait /\[Y\/n\]:/
Sleep 1s
Enter
Sleep 1s
Wait
Sleep 1s
Type "mote doctor"
Enter
Wait
Sleep 5s
