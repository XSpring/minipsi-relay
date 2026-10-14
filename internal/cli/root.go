// Package cli implements the minipsi command line: receiver, sender,
// serve, request, certs dev, bench and version, with process exit codes 0–5.
package cli

import (
	"context"
	"errors"
	"fmt"
	"io"
	"runtime"

	"github.com/spf13/cobra"

	"github.com/XSpring/minipsi/internal/protocol"
	"github.com/XSpring/minipsi/internal/version"
)

// Exit codes.
const (
	ExitOK       = 0
	ExitUsage    = 1 // usage / configuration
	ExitInput    = 2 // input data
	ExitNetwork  = 3 // network / peer unavailable / timeout
	ExitProtocol = 4 // protocol violation / peer abort / interrupted
	ExitInternal = 5 // internal error
)

// exitError carries an explicit exit code.
type exitError struct {
	code int
	err  error
}

func (e *exitError) Error() string { return e.err.Error() }
func (e *exitError) Unwrap() error { return e.err }

// exitCode maps an error to the process exit code.
func exitCode(err error) int {
	if err == nil {
		return ExitOK
	}
	var ee *exitError
	if errors.As(err, &ee) {
		return ee.code
	}
	var te *protocol.TransportError
	if errors.As(err, &te) {
		return ExitNetwork
	}
	var pe *protocol.Error
	if errors.As(err, &pe) {
		if pe.Remote {
			return ExitProtocol
		}
		switch pe.Code {
		case protocol.CodeTimeout:
			return ExitNetwork
		case protocol.CodeInput:
			return ExitInput
		case protocol.CodeInternal:
			return ExitInternal
		}
		return ExitProtocol
	}
	if errors.Is(err, context.Canceled) {
		return ExitProtocol
	}
	return ExitInternal
}

// NewRoot builds the command tree writing to stdout/stderr.
func NewRoot(stdout, stderr io.Writer) *cobra.Command {
	root := &cobra.Command{
		Use:           "minipsi",
		Short:         "Semi-honest polynomial Diffie-Hellman private set intersection",
		SilenceUsage:  true,
		SilenceErrors: true,
	}
	root.SetOut(stdout)
	root.SetErr(stderr)
	root.AddCommand(newPartyCmd("receiver", stdout, stderr), newPartyCmd("sender", stdout, stderr),
		newPartyCmd("request", stdout, stderr), newServeCmd(stdout, stderr),
		newCertsCmd(stdout), newBenchCmd(stdout), newVersionCmd(stdout))
	return root
}

// Run executes the CLI with args and returns the exit code.
func Run(ctx context.Context, args []string, stdout, stderr io.Writer) int {
	root := NewRoot(stdout, stderr)
	root.SetArgs(args)
	err := root.ExecuteContext(ctx)
	if err == nil {
		return ExitOK
	}
	code := exitCode(err)
	var ee *exitError
	if !errors.As(err, &ee) && code == ExitInternal && !isRunError(err) {
		// cobra usage errors (unknown flag/command, bad flag value)
		code = ExitUsage
	}
	fmt.Fprintf(stderr, "minipsi: %v\n", err)
	return code
}

// isRunError reports whether err came from a protocol run rather than argument parsing.
func isRunError(err error) bool {
	var pe *protocol.Error
	var te *protocol.TransportError
	return errors.As(err, &pe) || errors.As(err, &te)
}

func newVersionCmd(stdout io.Writer) *cobra.Command {
	return &cobra.Command{
		Use:   "version",
		Short: "Print version, build and protocol information",
		Args:  cobra.NoArgs,
		Run: func(*cobra.Command, []string) {
			fmt.Fprintf(stdout, "minipsi %s\ncommit:   %s\nbuilt:    %s\nprotocol: v%d\nsuite:    %s\ngo:       %s %s/%s\n",
				version.Version, version.Commit, version.Date, version.ProtocolVersion, version.Suite,
				runtime.Version(), runtime.GOOS, runtime.GOARCH)
		},
	}
}
