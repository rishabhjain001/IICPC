// Package config loads and validates environment-variable configuration for
// the Build Pipeline service.
package config

import (
	"fmt"
	"os"
)

// Config holds all runtime configuration for the Build Pipeline service.
type Config struct {
	// DatabaseURL is the full libpq-compatible connection string, e.g.
	// "postgres://user:pass@host:5432/dbname".
	DatabaseURL string

	// KubeConfig is an optional path to a kubeconfig file. When empty the
	// service falls back to the in-cluster service-account credentials.
	KubeConfig string

	// ArtifactRegistryURL is the base URL of the Artifact Registry, e.g.
	// "https://registry.internal:5000".
	ArtifactRegistryURL string

	// BuildNamespace is the Kubernetes namespace in which build Jobs are
	// created. Defaults to "dbhp-builds".
	BuildNamespace string
}

// Load reads configuration from environment variables. It returns an error if
// any required variable is absent.
func Load() (*Config, error) {
	databaseURL := os.Getenv("DATABASE_URL")
	if databaseURL == "" {
		return nil, fmt.Errorf("required environment variable DATABASE_URL is not set")
	}

	artifactRegistryURL := os.Getenv("ARTIFACT_REGISTRY_URL")
	if artifactRegistryURL == "" {
		return nil, fmt.Errorf("required environment variable ARTIFACT_REGISTRY_URL is not set")
	}

	buildNamespace := os.Getenv("BUILD_NAMESPACE")
	if buildNamespace == "" {
		buildNamespace = "dbhp-builds"
	}

	return &Config{
		DatabaseURL:         databaseURL,
		KubeConfig:          os.Getenv("KUBECONFIG"), // optional
		ArtifactRegistryURL: artifactRegistryURL,
		BuildNamespace:      buildNamespace,
	}, nil
}
