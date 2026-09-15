// Command wt hands out git worktrees from a capped pool.
//
// One pool per repository, five worktrees each, recycled least-recently-used
// and never while something is working inside one.
package main

import (
	"os"

	"github.com/alexdepape/wt/internal/cli"
)

func main() {
	os.Exit(cli.Run(os.Args[1:], os.Stdout))
}
