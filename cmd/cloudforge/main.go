// Package main provides the CloudForge executable.
package main

import (
	"context"
	"os"

	"github.com/noor15102002/cloud-forge/internal/cli"
)

func main() {
	os.Exit(cli.Execute(context.Background(), os.Args[1:], os.Stdout, os.Stderr))
}
