// Package cli wires the khook cobra commands: apply, plan, validate,
// schema, version.
package cli

import (
	"fmt"
	"log/slog"
	"os"
	"strings"

	"github.com/spf13/cobra"
)

// Exit codes: validation problems (bad spec, unresolved variables) exit 2;
// execution failures exit 1.
const (
	ExitExecution  = 1
	ExitValidation = 2
)

// CodedError carries the process exit code for main.
type CodedError struct {
	Code int
	Err  error
}

func (e *CodedError) Error() string { return e.Err.Error() }
func (e *CodedError) Unwrap() error { return e.Err }

func validationErr(err error) error { return &CodedError{Code: ExitValidation, Err: err} }
func executionErr(err error) error  { return &CodedError{Code: ExitExecution, Err: err} }

type rootOptions struct {
	kubeconfig  string
	kubecontext string
	logLevel    string
	logFormat   string
	// logLevelSet records whether --log-level was given explicitly (the
	// interactive progress display quiets logging only when it was not).
	logLevelSet bool

	log *slog.Logger
}

// NewRootCommand builds the khook command tree.
func NewRootCommand() *cobra.Command {
	opts := &rootOptions{}
	root := &cobra.Command{
		Use:           "khook",
		Short:         "cloud-init for Kubernetes: initialize a fresh cluster from a declarative spec",
		SilenceUsage:  true,
		SilenceErrors: true,
		PersistentPreRunE: func(cmd *cobra.Command, args []string) error {
			log, err := newLogger(opts.logLevel, opts.logFormat)
			if err != nil {
				return validationErr(err)
			}
			opts.log = log
			opts.logLevelSet = cmd.Root().PersistentFlags().Changed("log-level")
			slog.SetDefault(log)
			return nil
		},
	}
	flags := root.PersistentFlags()
	flags.StringVar(&opts.kubeconfig, "kubeconfig", "", "path to the kubeconfig file (default: standard loading rules)")
	flags.StringVar(&opts.kubecontext, "context", "", "kubeconfig context to use")
	flags.StringVar(&opts.logLevel, "log-level", "info", "log level: debug, info, warn, error")
	flags.StringVar(&opts.logFormat, "log-format", "text", "log format: text or json")

	root.AddCommand(
		newApplyCommand(opts),
		newPlanCommand(opts),
		newValidateCommand(opts),
		newSchemaCommand(),
		newVersionCommand(),
	)
	return root
}

func newLogger(level, format string) (*slog.Logger, error) {
	var lvl slog.Level
	if err := lvl.UnmarshalText([]byte(level)); err != nil {
		return nil, fmt.Errorf("invalid --log-level %q", level)
	}
	handlerOpts := &slog.HandlerOptions{Level: lvl}
	switch strings.ToLower(format) {
	case "text":
		return slog.New(slog.NewTextHandler(os.Stderr, handlerOpts)), nil
	case "json":
		return slog.New(slog.NewJSONHandler(os.Stderr, handlerOpts)), nil
	}
	return nil, fmt.Errorf("invalid --log-format %q (want text or json)", format)
}
