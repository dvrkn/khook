// Package cli wires the khook cobra commands: apply, plan, validate,
// graph, schema, version.
package cli

import (
	"errors"
	"fmt"
	"io"
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
	// redact masks secret variable values (KHOOK_SECRET_*) in all output.
	// Created empty here; specFlags.variables() registers values into it.
	redact *redactor
}

// NewRootCommand builds the khook command tree.
func NewRootCommand() *cobra.Command {
	opts := &rootOptions{redact: &redactor{}}
	root := &cobra.Command{
		Use:           "khook",
		Short:         "Bootstrap a fresh Kubernetes cluster from a declarative spec",
		SilenceUsage:  true,
		SilenceErrors: true,
		PersistentPreRunE: func(cmd *cobra.Command, args []string) error {
			log, err := newLogger(opts.logLevel, opts.logFormat, opts.redact.Wrap(os.Stderr))
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
		newDestroyCommand(opts),
		newPlanCommand(opts),
		newValidateCommand(opts),
		newStatusCommand(opts),
		newGraphCommand(opts),
		newSchemaCommand(),
		newVersionCommand(),
	)
	for _, sub := range root.Commands() {
		redactRunE(sub, opts.redact)
	}
	return root
}

// redactRunE wraps a command's RunE so error text leaving the cli package
// (printed by main) is masked too — executor errors can embed manifest
// content. The CodedError exit code survives the rewrite.
func redactRunE(cmd *cobra.Command, r *redactor) {
	orig := cmd.RunE
	if orig == nil {
		return
	}
	cmd.RunE = func(c *cobra.Command, args []string) error {
		err := orig(c, args)
		if err == nil {
			return nil
		}
		masked := r.String(err.Error())
		if masked == err.Error() {
			return err
		}
		var coded *CodedError
		if errors.As(err, &coded) {
			return &CodedError{Code: coded.Code, Err: errors.New(masked)}
		}
		return errors.New(masked)
	}
}

func newLogger(level, format string, w io.Writer) (*slog.Logger, error) {
	var lvl slog.Level
	if err := lvl.UnmarshalText([]byte(level)); err != nil {
		return nil, fmt.Errorf("invalid --log-level %q", level)
	}
	handlerOpts := &slog.HandlerOptions{Level: lvl}
	switch strings.ToLower(format) {
	case "text":
		return slog.New(slog.NewTextHandler(w, handlerOpts)), nil
	case "json":
		return slog.New(slog.NewJSONHandler(w, handlerOpts)), nil
	}
	return nil, fmt.Errorf("invalid --log-format %q (want text or json)", format)
}
