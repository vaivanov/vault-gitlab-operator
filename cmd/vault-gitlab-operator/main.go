// vault-gitlab-operator syncs secrets from HashiCorp Vault (KV v2) into
// GitLab CI/CD variables at instance, group and project level.
package main

import (
	"errors"
	"fmt"
	"log/slog"
	"os"

	"github.com/spf13/cobra"

	"github.com/vaivanov/vault-gitlab-operator/internal/config"
	"github.com/vaivanov/vault-gitlab-operator/internal/logging"
)

// version is injected at build time via -ldflags.
var version = "dev"

// Exit codes shared by all subcommands.
const (
	exitOK          = 0
	exitSyncErrors  = 1
	exitConfigError = 2
	exitDiffPending = 3
)

type rootFlags struct {
	configPath string
	logLevel   string
	logFormat  string
}

func main() {
	os.Exit(run(os.Args[1:]))
}

func run(args []string) int {
	flags := &rootFlags{}

	root := &cobra.Command{
		Use:           "vault-gitlab-operator",
		Short:         "Sync secrets from HashiCorp Vault into GitLab CI/CD variables",
		Version:       version,
		SilenceUsage:  true,
		SilenceErrors: true,
	}
	root.PersistentFlags().StringVarP(&flags.configPath, "config", "c", "config.yaml", "path to YAML config file")
	root.PersistentFlags().StringVar(&flags.logLevel, "log-level", "info", "log level (debug|info|warn|error)")
	root.PersistentFlags().StringVar(&flags.logFormat, "log-format", "text", "log format (text|json)")

	root.AddCommand(newValidateCmd(flags))
	root.AddCommand(newOnceCmd(flags))
	root.AddCommand(newDiffCmd(flags))
	root.AddCommand(newDaemonCmd(flags))

	root.SetArgs(args)
	if err := root.Execute(); err != nil {
		fmt.Fprintln(os.Stderr, "error:", err)
		var coded *exitError
		if errors.As(err, &coded) {
			return coded.code
		}
		return exitConfigError
	}
	return exitOK
}

// exitError carries a specific process exit code through cobra.
type exitError struct {
	code int
	err  error
}

func (e *exitError) Error() string { return e.err.Error() }
func (e *exitError) Unwrap() error { return e.err }

func setupLogger(flags *rootFlags) (*slog.Logger, error) {
	return logging.Setup(os.Stderr, flags.logLevel, flags.logFormat)
}

func newValidateCmd(flags *rootFlags) *cobra.Command {
	return &cobra.Command{
		Use:   "validate",
		Short: "Parse and validate the config file, then exit",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			log, err := setupLogger(flags)
			if err != nil {
				return &exitError{code: exitConfigError, err: err}
			}

			cfg, err := config.Load(flags.configPath)
			if err != nil {
				return &exitError{code: exitConfigError, err: err}
			}

			for _, w := range cfg.Warnings {
				log.Warn(w)
			}

			vars, fromSecrets := 0, 0
			for _, t := range cfg.Expanded {
				for _, v := range t.Variables {
					if v.FromSecret != nil {
						fromSecrets++
					} else {
						vars++
					}
				}
			}
			fmt.Fprintf(cmd.OutOrStdout(),
				"config OK: %d target(s), %d variable(s), %d from_secret mapping(s), %d warning(s)\n",
				len(cfg.Expanded), vars, fromSecrets, len(cfg.Warnings))
			return nil
		},
	}
}
