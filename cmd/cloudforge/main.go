// Package main provides the CloudForge executable.
package main

import (
	"context"
	"os"
	"os/signal"
	"syscall"

	"github.com/noor15102002/cloud-forge/internal/cli"
)

func main() {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	os.Exit(cli.Execute(ctx, os.Args[1:], os.Stdout, os.Stderr))
}
