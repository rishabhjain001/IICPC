// Package config loads Submission Engine configuration from environment variables.
package config

import "os"

// Config holds all runtime configuration for the Submission Engine.
type Config struct {
	// DatabaseURL is the DSN for the PostgreSQL connection pool (pgx format).
	DatabaseURL string

	// RedisURL is the connection URL for Redis (redis://host:port).
	RedisURL string

	// Port is the HTTPS listen port (default "8443").
	Port string

	// TLSCertFile is the path to the TLS certificate PEM file.
	TLSCertFile string

	// TLSKeyFile is the path to the TLS private key PEM file.
	TLSKeyFile string

	// ArtifactRegistryURL is the base URL of the OCI distribution-spec v1
	// registry (default "http://localhost:5000"). Ignored when
	// ArtifactRegistryDir is set.
	ArtifactRegistryURL string

	// ArtifactRegistryDir enables filesystem-backed registry mode. When
	// non-empty, artifacts are stored as files under this directory instead of
	// being pushed to an OCI registry.
	ArtifactRegistryDir string
}

// Load reads configuration from environment variables, applying defaults where
// a value is not set. It returns the populated Config; callers are responsible
// for validating that required fields are non-empty before starting the server.
func Load() Config {
	port := os.Getenv("PORT")
	if port == "" {
		port = "8443"
	}

	registryURL := os.Getenv("ARTIFACT_REGISTRY_URL")
	if registryURL == "" {
		registryURL = "http://localhost:5000"
	}

	return Config{
		DatabaseURL:         os.Getenv("DATABASE_URL"),
		RedisURL:            os.Getenv("REDIS_URL"),
		Port:                port,
		TLSCertFile:         os.Getenv("TLS_CERT_FILE"),
		TLSKeyFile:          os.Getenv("TLS_KEY_FILE"),
		ArtifactRegistryURL: registryURL,
		ArtifactRegistryDir: os.Getenv("ARTIFACT_REGISTRY_DIR"),
	}
}
