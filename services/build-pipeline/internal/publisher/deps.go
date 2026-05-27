// Package publisher handles post-build OCI image assembly and push to the
// Artifact Registry.
//
// Requirements: 2.5, 2.6
package publisher

import (
	"bytes"
	"context"
	"fmt"
	"os/exec"
	"strings"

	"github.com/iicpc/dbhp/build-pipeline/internal/executor"
)

// EnumerateDeps returns the list of absolute paths of dynamic library
// dependencies for the given binary, based on its manifest type.
//
//   - ManifestMakefile / ManifestCargoToml: runs ldd and parses lines of the
//     form "libfoo.so.6 => /lib/x86_64-linux-gnu/libfoo.so.6 (0x...)"
//     extracting only the resolved (right-hand) absolute path.
//   - ManifestGoMod: Go binaries are statically linked by default; returns an
//     empty slice (go tool nm confirms the binary but no deps are needed).
//
// Virtual pseudo-entries (linux-vdso.so, entries without "=>", and
// "not found" entries) are silently filtered.
func EnumerateDeps(ctx context.Context, binaryPath string, manifestType executor.ManifestType) ([]string, error) {
	switch manifestType {
	case executor.ManifestGoMod:
		// Confirm the binary exists / is readable by running go tool nm, but
		// Go binaries are statically linked by default — no dynamic deps.
		if err := runGoToolNm(ctx, binaryPath); err != nil {
			return nil, fmt.Errorf("publisher: go tool nm %s: %w", binaryPath, err)
		}
		return []string{}, nil

	case executor.ManifestMakefile, executor.ManifestCargoToml:
		return runLdd(ctx, binaryPath)

	default:
		return nil, fmt.Errorf("publisher: unsupported manifest type %q", manifestType)
	}
}

// runLdd executes ldd against binaryPath and parses the output to extract
// resolved absolute dependency paths.
func runLdd(ctx context.Context, binaryPath string) ([]string, error) {
	//nolint:gosec // binaryPath originates from controlled internal WorkDir.
	cmd := exec.CommandContext(ctx, "ldd", binaryPath)
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr

	if err := cmd.Run(); err != nil {
		return nil, fmt.Errorf("publisher: ldd failed: %w; stderr: %s", err, stderr.String())
	}

	return parseLddOutput(stdout.String()), nil
}

// parseLddOutput parses raw ldd stdout and returns resolved absolute library
// paths.  Lines with the pattern:
//
//	libfoo.so.6 => /path/to/libfoo.so.6 (0x00007f...)
//
// are accepted.  Lines without "=>" (e.g. linux-vdso.so pseudo entries and
// the ld-linux linker line) and lines where the resolved path is "not found"
// are filtered out.
func parseLddOutput(output string) []string {
	var deps []string
	for _, line := range strings.Split(output, "\n") {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}

		// Only process lines that contain "=>" (resolved dynamic libs).
		idx := strings.Index(line, "=>")
		if idx < 0 {
			// e.g. "	/lib64/ld-linux-x86-64.so.2 (0x...)" or linux-vdso
			continue
		}

		// Everything after "=>" up to the address in parentheses.
		after := strings.TrimSpace(line[idx+2:])

		// Strip the trailing " (0x...)" address annotation.
		if parenIdx := strings.Index(after, " ("); parenIdx >= 0 {
			after = strings.TrimSpace(after[:parenIdx])
		}

		// Filter pseudo/virtual entries.
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
