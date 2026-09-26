Output docs/demo/patch.gif
Set Height 900
Source docs/demo/common.tape
Hide
Type 'cat shop/cart.py'
Show
Sleep 600ms
Enter
Wait
Sleep 1s
Hide
Type 'mote run patch shop "Add a function total(items) that sums price * qty" --apply'
Show
Sleep 600ms
Enter
Wait
Sleep 1s
Hide
Type 'git -C shop diff --stat'
Show
Sleep 600ms
Enter
Wait
Sleep 4s
