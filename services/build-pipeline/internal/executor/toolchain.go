package executor

import "fmt"

// BuildCommand describes the executable and arguments used to build a
// submission artifact for a specific manifest type.
type BuildCommand struct {
	// Binary is the program to execute, e.g. "make", "cargo", "go".
	Binary string

	// Args are the arguments passed to Binary, e.g. ["build", "--release"].
	Args []string

	// Env holds additional environment variables that must be set for the
	// build, in "KEY=VALUE" format.  These are merged with (not replacing)
	// the process environment when the command is run.
	Env []string
}

// BuildCommandFor returns the appropriate BuildCommand for the given manifest
// type, using the officially pinned toolchain versions:
//
//   - Makefile   → make                       (GCC 13 / Clang 17, pre-installed in image)
//   - Cargo.toml → cargo build --release      (Rust 1.77, pinned via RUSTUP_TOOLCHAIN)
//   - go.mod     → go build ./...             (Go 1.22, pinned via GOVERSION)
//
// Returns an error for ManifestUnknown or any unrecognised ManifestType.
func BuildCommandFor(manifest ManifestType) (BuildCommand, error) {
	switch manifest {
	case ManifestMakefile:
		// The build image ships gcc-13 and clang-17; make orchestrates the
		// build without extra toolchain flags from our side (Requirement 2.2).
		return BuildCommand{
			Binary: "make",
			Args:   []string{},
			Env:    []string{},
		}, nil

	case ManifestCargoToml:
		// RUSTUP_TOOLCHAIN=1.77 pins the exact Rust stable release used during
		// the build, regardless of the default toolchain installed in the image
		// (Requirement 2.2).  --release produces the optimised binary required
		// for benchmarking.
		return BuildCommand{
			Binary: "cargo",
			Args:   []string{"build", "--release"},
			Env:    []string{"RUSTUP_TOOLCHAIN=1.77"},
		}, nil

	case ManifestGoMod:
		// GOVERSION=1.22 pins the Go toolchain version used by go build
		// (Requirement 2.2).  ./... builds all packages in the module so the
		// contestant need not specify a main package path.
		return BuildCommand{
			Binary: "go",
			Args:   []string{"build", "./..."},
			Env:    []string{"GOVERSION=1.22"},
		}, nil

	case ManifestUnknown:
		return BuildCommand{}, fmt.Errorf("executor: unknown manifest type — cannot select toolchain")

	default:
		return BuildCommand{}, fmt.Errorf("executor: unsupported manifest type %q", manifest)
	}
}
