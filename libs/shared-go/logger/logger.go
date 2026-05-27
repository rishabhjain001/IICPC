// Package logger provides a shared structured JSON logger for all DBHP
// microservices, built on top of go.uber.org/zap.
//
// Usage:
//
//	log, err := logger.New("info")
//	if err != nil { ... }
//	defer log.Sync()
//
//	log = logger.WithRunID(log, runID)
//	log = logger.WithSubmissionID(log, submissionID)
//	log.Info("processing submission", zap.String("artifact_size", "42MB"))
package logger

import (
	"fmt"

	"go.uber.org/zap"
	"go.uber.org/zap/zapcore"
)

// New creates a production zap logger that writes structured JSON to stdout.
// level controls the minimum log level; accepted values are "debug", "info",
// "warn", "error", "dpanic", "panic", "fatal".  An empty string defaults to
// "info".
func New(level string) (*zap.Logger, error) {
	if level == "" {
		level = "info"
	}

	var zapLevel zapcore.Level
	if err := zapLevel.UnmarshalText([]byte(level)); err != nil {
		return nil, fmt.Errorf("logger.New: invalid level %q: %w", level, err)
	}

	cfg := zap.NewProductionConfig()
	cfg.Level = zap.NewAtomicLevelAt(zapLevel)
	// Ensure output always goes to stdout (not a file) — production default is
	// already "stdout", but we make it explicit for clarity.
	cfg.OutputPaths = []string{"stdout"}
	cfg.ErrorOutputPaths = []string{"stderr"}

	log, err := cfg.Build()
	if err != nil {
		return nil, fmt.Errorf("logger.New: build logger: %w", err)
	}
	return log, nil
}

// WithRunID returns a child logger that includes benchmark_run_id as a
// structured field in every log entry.  Use this wherever the benchmark run
// context is available to enable log-based correlation across services.
func WithRunID(log *zap.Logger, runID string) *zap.Logger {
	return log.With(zap.String("benchmark_run_id", runID))
}

// WithSubmissionID returns a child logger that includes submission_id as a
// structured field in every log entry.  Use this in the Submission Engine,
// Build Pipeline, and Sandbox Controller whenever a specific submission is
// being acted upon.
func WithSubmissionID(log *zap.Logger, submissionID string) *zap.Logger {
	return log.With(zap.String("submission_id", submissionID))
}
