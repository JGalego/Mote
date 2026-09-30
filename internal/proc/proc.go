// Package proc runs commands so that stopping one stops what it started.
//
// A command run through the shell starts other commands, and killing the shell
// leaves them running. Tree makes a command's context, when it ends, kill the
// command and everything it has started. The command stays in mote's own
// process group, so it keeps the terminal it was given: it can read a password
// from /dev/tty, and it is hung up with mote when the terminal closes.
package proc

import "time"

// killGrace is how long Wait waits for a command's output pipes to close after
// it is killed, since a process that escaped can hold them open.
const killGrace = 2 * time.Second
