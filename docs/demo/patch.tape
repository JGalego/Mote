Output docs/demo/patch.gif
Source docs/demo/common.tape
Type "cat shop/cart.py"
Enter
Wait
Type 'mote run patch shop "Add a function total(items) that sums price * qty" --apply'
Sleep 500ms
Enter
Wait
Type "git -C shop diff --stat"
Enter
Wait
Sleep 4s
