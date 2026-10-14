// Command minipsi runs the semi-honest Poly-DH PSI protocol between two hosts.
package main

import (
	"context"
	"os"

	"github.com/XSpring/minipsi/internal/cli"
)

func main() {
	os.Exit(cli.Run(context.Background(), os.Args[1:], os.Stdout, os.Stderr))
}
