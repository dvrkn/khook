// khook is declarative bootstrap for Kubernetes: one static binary that initializes a
// freshly created cluster from a declarative YAML spec.
package main

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/signal"
	"syscall"

	"github.com/dvrkn/khook/internal/cli"
)

func main() {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	root := cli.NewRootCommand()
	err := root.ExecuteContext(ctx)
	if err == nil {
		return
	}
	fmt.Fprintln(os.Stderr, "Error:", err)
	var coded *cli.CodedError
	if errors.As(err, &coded) {
		os.Exit(coded.Code)
	}
	os.Exit(1)
}
