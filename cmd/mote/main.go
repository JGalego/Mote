// Command mote runs local, CPU-only X-to-Y AI tasks.
package main

import (
	"os"

	"github.com/jgalego/mote/internal/cli"
)

func main() {
	os.Exit(cli.Main(os.Args[1:], os.Stdin, os.Stdout, os.Stderr))
}
