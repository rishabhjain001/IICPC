// Package deps enumerates dynamic library dependencies for a compiled binary.
//
// Requirements: 2.5
package deps

import (
	"bytes"
	"context"
	"fmt"
	"os/exec"
	"strings"
)

// Toolchain constants mirror executor.ManifestType string values so the deps
// package does not import the executor package (avoiding a cycle).
const (
	ToolchainMakefile  = "Makefile"
	ToolchainCargoToml = "Cargo.toml"
	ToolchainGoMod     = "go.mod"
)

// EnumerateDeps returns the list of absolute paths of dynamic library
// dependencies for the given binary.
//
//   - C++ (Makefile) and Rust (Cargo.toml) binaries: runs ldd <binaryPath>
//     and parses lines of the form "libfoo.so.6 => /path/to/libfoo.so.6 (0x…)"
//     extracting only the resolved right-hand absolute path.
//   - Go binaries (go.mod): runs "go tool nm <binaryPath>" to confirm the
//     binary is readable; Go binaries are statically linked by default so an
//     empty slice is returned.
//
// Virtual pseudo-entries (linux-vdso.so, entries without "=>", and
// "not found" entries) are silently filtered.
//
// The toolchain parameter must be one of the Toolchain* constants above.
func EnumerateDeps(ctx context.Context, binaryPath string, toolchain string) ([]string, error) {
	switch toolchain {
	case ToolchainGoMod:
		// Go binaries are statically linked by default.  Run go tool nm to
		// confirm the binary is well-formed; discard the output.
		if err := runGoToolNm(ctx, binaryPath); err != nil {
			return nil, fmt.Errorf("deps: go tool nm %s: %w", binaryPath, err)
		}
		return []string{}, nil

	case ToolchainMakefile, ToolchainCargoToml:
		return runLdd(ctx, binaryPath)

	default:
		return nil, fmt.Errorf("deps: unsupported toolchain %q", toolchain)
	}
}

// runLdd executes ldd against binaryPath and returns the resolved absolute
// library paths from its output.
func runLdd(ctx context.Context, binaryPath string) ([]string, error) {
	//nolint:gosec // binaryPath originates from controlled internal WorkDir.
	cmd := exec.CommandContext(ctx, "ldd", binaryPath)
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr

	if err := cmd.Run(); err != nil {
		return nil, fmt.Errorf("deps: ldd failed: %w; stderr: %s", err, stderr.String())
	}

	return parseLddOutput(stdout.String()), nil
}

// parseLddOutput parses raw ldd stdout and returns resolved absolute library
// paths.  Only lines with the pattern:
//
//	libfoo.so.6 => /path/to/libfoo.so.6 (0x00007f…)
//
// are kept.  Lines without "=>" (e.g. linux-vdso.so pseudo entries and the
// ld-linux dynamic linker row) and lines whose resolved path is "not found"
// are filtered out.
func parseLddOutput(output string) []string {
	var deps []string
	for _, line := range strings.Split(output, "\n") {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}

		idx := strings.Index(line, "=>")
		if idx < 0 {
			continue
		}

		// Everything after "=>" up to the trailing " (0x…)" annotation.
		after := strings.TrimSpace(line[idx+2:])
		if parenIdx := strings.Index(after, " ("); parenIdx >= 0 {
			after = strings.TrimSpace(after[:parenIdx])
		}

		if after == "" || after == "not found" {
			continue
		}

		deps = append(deps, after)
	}
	return deps
}

// runGoToolNm calls "go tool nm <binaryPath>" to verify the binary is
// well-formed.  The output is discarded; only the exit status matters.
func runGoToolNm(ctx context.Context, binaryPath string) error {
	//nolint:gosec // binaryPath originates from controlled internal WorkDir.
	cmd := exec.CommandContext(ctx, "go", "tool", "nm", binaryPath)
	var stderr bytes.Buffer
	cmd.Stderr = &stderr

	if err := cmd.Run(); err != nil {
		return fmt.Errorf("%w; stderr: %s", err, stderr.String())
	}
	return nil
}
