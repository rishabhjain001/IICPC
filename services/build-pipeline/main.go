// Package main is the entry point for the Build Pipeline (Hermetic Job
// Controller) service.
//
// The service watches the submissions table for rows in status UPLOADED and
// spawns a hermetic Kubernetes build Job for each one (Requirement 2.1, 2.2).
package main

import (
	"context"
	"fmt"
	"os"
	"os/signal"
	"syscall"

	"github.com/jackc/pgx/v5/pgxpool"
	"go.uber.org/zap"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/rest"
	"k8s.io/client-go/tools/clientcmd"

	"github.com/iicpc/dbhp/build-pipeline/internal/config"
	"github.com/iicpc/dbhp/build-pipeline/internal/spawner"
	"github.com/iicpc/dbhp/build-pipeline/internal/watcher"
)

func main() {
	if err := run(); err != nil {
		fmt.Fprintf(os.Stderr, "build-pipeline: fatal: %v\n", err)
		os.Exit(1)
	}
}

func run() error {
	// ------------------------------------------------------------------ logger
	logger, err := zap.NewProduction()
	if err != nil {
		return fmt.Errorf("init logger: %w", err)
	}
	defer logger.Sync() //nolint:errcheck

	// ------------------------------------------------------------------ config
	cfg, err := config.Load()
	if err != nil {
		return fmt.Errorf("load config: %w", err)
	}

	// --------------------------------------------------------------- database
	ctx, cancel := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer cancel()

	pool, err := pgxpool.New(ctx, cfg.DatabaseURL)
	if err != nil {
		return fmt.Errorf("open database pool: %w", err)
	}
	defer pool.Close()

	if err := pool.Ping(ctx); err != nil {
		return fmt.Errorf("ping database: %w", err)
	}
	logger.Info("database pool ready")

	// ------------------------------------------------------------ k8s client
	k8sClient, err := newK8sClient(cfg.KubeConfig)
	if err != nil {
		// When running outside a cluster (e.g. docker compose) k8s is
		// unavailable.  Degrade gracefully — warn and idle.
		logger.Warn("kubernetes not available — running in no-op mode",
			zap.Error(err),
		)
		logger.Info("build-pipeline idling (no kubernetes)")
		<-ctx.Done()
		logger.Info("build-pipeline stopped")
		return nil
	}
	logger.Info("kubernetes client ready")

	// --------------------------------------------------------------- spawner
	s := spawner.New(
		k8sClient,
		pool,
		cfg.BuildNamespace,
		cfg.ArtifactRegistryURL,
		logger,
	)

	// --------------------------------------------------------------- watcher
	w := watcher.New(pool, s, logger)

	logger.Info("build-pipeline starting",
		zap.String("namespace", cfg.BuildNamespace),
		zap.String("artifact_registry_url", cfg.ArtifactRegistryURL),
	)

	w.Run(ctx) // blocks until ctx is cancelled
	logger.Info("build-pipeline stopped")
	return nil
}

// newK8sClient returns a Kubernetes client using kubeConfigPath if provided,
// falling back to in-cluster configuration otherwise.
func newK8sClient(kubeConfigPath string) (kubernetes.Interface, error) {
	var restCfg *rest.Config
	var err error

	if kubeConfigPath != "" {
		restCfg, err = clientcmd.BuildConfigFromFlags("", kubeConfigPath)
	} else {
		restCfg, err = rest.InClusterConfig()
	}
	if err != nil {
		return nil, err
	}

	return kubernetes.NewForConfig(restCfg)
}
