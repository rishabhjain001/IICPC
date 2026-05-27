// Package executor handles build command selection, execution, and log capture
// for the DBHP Build Pipeline service.
package executor

// ManifestType identifies which build system the submitted source archive uses.
type ManifestType string

const (
	// ManifestMakefile indicates a root-level Makefile (C++ / GCC 13 / Clang 17).
	ManifestMakefile ManifestType = "Makefile"

	// ManifestCargoToml indicates a root-level Cargo.toml (Rust 1.77).
	ManifestCargoToml ManifestType = "Cargo.toml"

	// ManifestGoMod indicates a root-level go.mod (Go 1.22).
	ManifestGoMod ManifestType = "go.mod"

	// ManifestUnknown is returned when none of the three known manifests are
	// found at the root level of the artifact directory.
	ManifestUnknown ManifestType = ""
)

// DetectManifest scans the list of filenames in an extracted artifact directory
// and returns the manifest type for the first recognised root-level manifest
// file.  Only exact basename matches at the root (i.e. no directory separator
// in the name) are considered.
//
// Priority: Cargo.toml (Rust) > go.mod (Go) > Makefile (C++).
// Returns ManifestUnknown if none of the three are present.
func DetectManifest(files []string) ManifestType {
	var (
		hasMakefile  bool
		hasCargoToml bool
		hasGoMod     bool
	)

	for _, f := range files {
		// Only root-level files: reject anything with a path separator.
		if containsPathSeparator(f) {
			continue
		}
		switch f {
		case "Makefile":
			hasMakefile = true
		case "Cargo.toml":
			hasCargoToml = true
		case "go.mod":
			hasGoMod = true
		}
	}

	switch {
	case hasCargoToml:
		return ManifestCargoToml
	case hasGoMod:
		return ManifestGoMod
	case hasMakefile:
		return ManifestMakefile
	default:
		return ManifestUnknown
	}
}

// containsPathSeparator reports whether s contains '/' or '\'.
func containsPathSeparator(s string) bool {
	for _, c := range s {
		if c == '/' || c == '\\' {
			return true
		}
	}
	return false
}
