package main

import (
	"context"
	"fmt"
	"os"
	"os/signal"
	"syscall"

	"github.com/immich-relay/immich-relay/internal/cli"
)

var version = "dev"

func main() {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	root := cli.New(version)
	root.SetArgs(os.Args[1:])
	root.SetContext(ctx)
	if err := root.Execute(); err != nil {
		fmt.Fprintln(os.Stderr, "immich-relay:", err)
		os.Exit(1)
	}
}
