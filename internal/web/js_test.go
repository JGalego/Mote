package web

import (
	"os/exec"
	"testing"
)

// The page's own logic that needs no browser is tested in JavaScript, where it
// runs; this runs those tests wherever there is a Node.
func TestPageScripts(t *testing.T) {
	node, err := exec.LookPath("node")
	if err != nil {
		t.Skip("no Node on the PATH")
	}
	for _, test := range []string{"jstest/highlight_test.js"} {
		if out, err := exec.Command(node, test).CombinedOutput(); err != nil {
			t.Errorf("%s failed: %v\n%s", test, err, out)
		}
	}
}
