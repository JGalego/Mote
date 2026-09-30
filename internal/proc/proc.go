// Package proc runs commands so that stopping one stops what it started.
//
// A command run through the shell starts other commands, and killing the shell
// leaves them running. Tree gives a command a process group of its own and
// kills the whole group when its context ends, so that stopping a cell, or
// pressing Ctrl-C, leaves nothing behind.
package proc

import "time"

// killGrace is how long Wait waits for a command's output pipes to close after
// it is killed, since a process that escaped the group can hold them open.
const killGrace = 2 * time.Second
