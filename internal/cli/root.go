// Package cli defines CloudForge's command-line interface.
package cli

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"runtime/debug"
	"strings"

	"github.com/spf13/cobra"

	"github.com/noor15102002/cloud-forge/internal/analyzer"
	"github.com/noor15102002/cloud-forge/internal/command"
	"github.com/noor15102002/cloud-forge/internal/doctor"
	"github.com/noor15102002/cloud-forge/internal/regression"
	"github.com/noor15102002/cloud-forge/internal/render"
	"github.com/noor15102002/cloud-forge/internal/verification"
	"github.com/noor15102002/cloud-forge/pkg/model"
)

var (
	// Version is the release version injected at build time.
	Version = "dev"
	// Commit is the source revision injected at build time.
	Commit = "unknown"
	// Date is the build date injected at build time.
	Date = "unknown"
)

type exitError struct {
	code int
	err  error
}

func (e *exitError) Error() string { return e.err.Error() }
func (e *exitError) Unwrap() error { return e.err }

// Execute runs the CLI and returns a process exit code.
func Execute(ctx context.Context, args []string, stdout, stderr io.Writer) int {
	root := NewRootCommand(stdout, stderr)
	root.SetArgs(args)
	if err := root.ExecuteContext(ctx); err != nil {
		var coded *exitError
		if errors.As(err, &coded) {
			if coded.err != nil && coded.code == 2 {
				_, _ = fmt.Fprintln(stderr, coded.err)
			}
			return coded.code
		}
		_, _ = fmt.Fprintln(stderr, err)
		return 2
	}
	return 0
}

// NewRootCommand constructs a CloudForge command tree for the supplied streams.
func NewRootCommand(stdout, stderr io.Writer) *cobra.Command {
	return newRootCommand(stdout, stderr, command.ExecRunner{})
}

func newRootCommand(stdout, stderr io.Writer, runner command.Runner) *cobra.Command {
	var verbose, debug bool
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	root := &cobra.Command{
		Use: "cloudforge", Short: "Verify production behavior in ephemeral Kubernetes environments",
		SilenceErrors: true, SilenceUsage: true,
		PersistentPreRun: func(_ *cobra.Command, _ []string) {
			level := slog.LevelError
			if verbose {
				level = slog.LevelInfo
			}
			if debug {
				level = slog.LevelDebug
			}
			logger = slog.New(slog.NewTextHandler(stderr, &slog.HandlerOptions{Level: level}))
		},
	}
	root.SetOut(stdout)
	root.SetErr(stderr)
	root.PersistentFlags().BoolVar(&verbose, "verbose", false, "show additional operational detail")
	root.PersistentFlags().BoolVar(&debug, "debug", false, "show debug diagnostics")
	getLogger := func() *slog.Logger { return logger }
	root.AddCommand(newVersionCommand(stdout), newDoctorCommand(stdout, getLogger, runner), newAnalyzeCommand(stdout, getLogger), newVerifyCommand(stdout, getLogger, runner), newReportCommand(stdout, getLogger))
	return root
}

func newVersionCommand(stdout io.Writer) *cobra.Command {
	var format string
	cmd := &cobra.Command{Use: "version", Short: "Print CloudForge version information", Args: cobra.NoArgs, RunE: func(_ *cobra.Command, _ []string) error {
		if err := validateFormat(format); err != nil {
			return &exitError{code: 2, err: err}
		}
		value := struct {
			SchemaVersion string `json:"schema_version"`
			Version       string `json:"version"`
			Commit        string `json:"commit"`
			Date          string `json:"date"`
		}{"v1alpha1", Version, Commit, Date}
		if format == "json" {
			return render.JSON(stdout, value)
		}
		_, err := fmt.Fprintf(stdout, "cloudforge %s (commit %s, built %s)\n", Version, Commit, Date)
		return err
	}}
	cmd.Flags().StringVar(&format, "format", "text", "output format: text or json")
	return cmd
}

func newDoctorCommand(stdout io.Writer, logger func() *slog.Logger, runner command.Runner) *cobra.Command {
	var format string
	cmd := &cobra.Command{Use: "doctor", Short: "Check dependencies required for verification", Args: cobra.NoArgs, RunE: func(cmd *cobra.Command, _ []string) error {
		if err := validateFormat(format); err != nil {
			return &exitError{code: 2, err: err}
		}
		logger().Info("checking CloudForge runtime dependencies")
		report := doctor.New(runner).Run(cmd.Context())
		logger().Debug("environment checks completed", "checks", len(report.Checks), "status", report.Status)
		var err error
		if format == "json" {
			err = render.JSON(stdout, report)
		} else {
			err = render.DoctorText(stdout, report)
		}
		if err != nil {
			return &exitError{code: 2, err: err}
		}
		if report.Status == "fail" {
			return &exitError{code: 1, err: fmt.Errorf("one or more required checks failed")}
		}
		if report.Status == "error" {
			return &exitError{code: 2, err: fmt.Errorf("one or more checks could not be executed")}
		}
		return nil
	}}
	cmd.Flags().StringVar(&format, "format", "text", "output format: text or json")
	return cmd
}

func newVerifyCommand(stdout io.Writer, logger func() *slog.Logger, runner command.Runner) *cobra.Command {
	var format string
	var keepEnvironment bool
	var baselinePath string
	var configPath string
	cmd := &cobra.Command{Use: "verify [path]", Short: "Build and verify an application in a disposable k3d cluster", Args: cobra.MaximumNArgs(1), RunE: func(cmd *cobra.Command, args []string) error {
		if err := validateVerificationFormat(format); err != nil {
			return &exitError{code: 2, err: err}
		}
		var baselineLoaded bool
		var baseline model.VerificationRun
		if baselinePath != "" {
			var err error
			baseline, err = regression.Load(baselinePath)
			if err != nil {
				return &exitError{code: 2, err: fmt.Errorf("CloudForge could not load the verification baseline: %w", err)}
			}
			baselineLoaded = true
		}
		path := "."
		if len(args) == 1 {
			path = args[0]
		}
		logger().Info("starting application verification", "path", path, "keep_environment", keepEnvironment)
		buildVersion, buildCommit := buildIdentity()
		outcome := verification.New(runner).Run(cmd.Context(), path, verification.Options{KeepEnvironment: keepEnvironment, ConfigPath: configPath, Version: buildVersion, Commit: buildCommit})
		if baselineLoaded {
			comparison := regression.Compare(outcome.Run, baseline)
			outcome.Run.Comparison = &comparison
		}
		logger().Debug("verification completed", "status", outcome.Run.Status, "evidence", len(outcome.Run.Evidence))
		var err error
		switch format {
		case "json":
			err = render.JSON(stdout, outcome.Run)
		case "markdown":
			err = render.VerificationMarkdown(stdout, outcome.Run)
		default:
			err = render.VerificationText(stdout, outcome.Run)
		}
		if err != nil {
			return &exitError{code: 2, err: err}
		}
		if outcome.ExitCode != 0 {
			return &exitError{code: outcome.ExitCode, err: fmt.Errorf("verification finished with status %s", outcome.Run.Status)}
		}
		if outcome.Run.Comparison != nil && outcome.Run.Comparison.Status == model.StatusFail {
			return &exitError{code: 1, err: errors.New("verification contains baseline regressions")}
		}
		return nil
	}}
	cmd.Flags().StringVar(&format, "format", "text", "output format: text, json, or markdown")
	cmd.Flags().StringVar(&configPath, "config", "", "strict verification configuration (default: application/cloudforge.yaml)")
	cmd.Flags().BoolVar(&keepEnvironment, "keep-environment", false, "keep the k3d cluster after verification")
	cmd.Flags().StringVar(&baselinePath, "baseline", "", "compare with an explicit v1alpha1 verification JSON file")
	return cmd
}

func newAnalyzeCommand(stdout io.Writer, logger func() *slog.Logger) *cobra.Command {
	var format string
	cmd := &cobra.Command{Use: "analyze [path]", Short: "Analyze supported application and deployment metadata", Args: cobra.MaximumNArgs(1), RunE: func(_ *cobra.Command, args []string) error {
		if err := validateFormat(format); err != nil {
			return &exitError{code: 2, err: err}
		}
		path := "."
		if len(args) == 1 {
			path = args[0]
		}
		logger().Info("analyzing repository", "path", path)
		result, err := analyzer.New().Analyze(path)
		if err != nil {
			return &exitError{code: 2, err: fmt.Errorf("CloudForge could not analyze the repository: %w", err)}
		}
		logger().Debug("repository analysis completed", "status", result.Status, "runtimes", len(result.Application.Runtimes), "diagnostics", len(result.Diagnostics))
		if format == "json" {
			err = render.JSON(stdout, result)
		} else {
			err = render.AnalysisText(stdout, result)
		}
		if err != nil {
			return &exitError{code: 2, err: err}
		}
		if !result.Supported {
			return &exitError{code: 1, err: fmt.Errorf("unsupported application")}
		}
		return nil
	}}
	cmd.Flags().StringVar(&format, "format", "text", "output format: text or json")
	return cmd
}

func newReportCommand(stdout io.Writer, logger func() *slog.Logger) *cobra.Command {
	var format string
	cmd := &cobra.Command{Use: "report <verification.json>", Short: "Render a trusted view of a verification JSON report", Args: cobra.ExactArgs(1), RunE: func(_ *cobra.Command, args []string) error {
		if err := validateVerificationFormat(format); err != nil {
			return &exitError{code: 2, err: err}
		}
		logger().Info("rendering verification report", "format", format)
		run, err := regression.Load(args[0])
		if err != nil {
			return &exitError{code: 2, err: fmt.Errorf("CloudForge could not load the verification report: %w", err)}
		}
		switch format {
		case "json":
			err = render.JSON(stdout, run)
		case "markdown":
			err = render.VerificationMarkdown(stdout, run)
		default:
			err = render.VerificationText(stdout, run)
		}
		if err != nil {
			return &exitError{code: 2, err: fmt.Errorf("CloudForge could not render the verification report: %w", err)}
		}
		return nil
	}}
	cmd.Flags().StringVar(&format, "format", "text", "output format: text, json, or markdown")
	return cmd
}

func validateFormat(value string) error {
	if value != "text" && value != "json" {
		return fmt.Errorf("unsupported format %q; use text or json", strings.TrimSpace(value))
	}
	return nil
}

func validateVerificationFormat(value string) error {
	if value != "text" && value != "json" && value != "markdown" {
		return fmt.Errorf("unsupported format %q; use text, json, or markdown", strings.TrimSpace(value))
	}
	return nil
}

func buildIdentity() (string, string) {
	version, commit := Version, Commit
	if commit == "unknown" {
		if info, ok := debug.ReadBuildInfo(); ok {
			for _, setting := range info.Settings {
				if setting.Key == "vcs.revision" {
					commit = setting.Value
				}
			}
			for _, setting := range info.Settings {
				if setting.Key == "vcs.modified" && setting.Value == "true" {
					commit += "+dirty"
				}
			}
		}
	}
	return version, commit
}
