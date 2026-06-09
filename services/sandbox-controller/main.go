// Package main is the entry point for the Sandbox Controller Kubernetes
// operator.  It wires together the reconciler, network manager, and any
// future controller-runtime machinery.
//
// Full operator scaffolding (leader-election, metrics server, controller
// runtime manager) is out of scope for Tasks 5.1/5.2; this file provides a
// runnable skeleton that confirms the package graph compiles cleanly.
package main

import (
	"context"
	"fmt"
	"os"
	"os/signal"
	"syscall"

	"go.uber.org/zap"
	"k8s.io/client-go/dynamic"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/rest"
	"k8s.io/client-go/tools/clientcmd"

	"github.com/iicpc/dbhp/sandbox-controller/internal/network"
)

func main() {
	log, err := zap.NewProduction()
	if err != nil {
		fmt.Fprintf(os.Stderr, "failed to build logger: %v\n", err)
		os.Exit(1)
	}
	defer log.Sync() //nolint:errcheck

	cfg, err := loadKubeConfig()
	if err != nil {
		// When running outside a cluster (e.g. docker compose) k8s is
		// unavailable.  Degrade gracefully — warn and idle.
		log.Warn("kubernetes not available — running in no-op mode",
			zap.Error(err),
		)
		ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
		defer stop()
		log.Info("sandbox-controller idling (no kubernetes)")
		<-ctx.Done()
		log.Info("sandbox-controller shutting down")
		return
	}

	k8sClient, err := kubernetes.NewForConfig(cfg)
	if err != nil {
		log.Fatal("failed to create kubernetes client", zap.Error(err))
	}

	dynamicClient, err := dynamic.NewForConfig(cfg)
	if err != nil {
		log.Fatal("failed to create dynamic client", zap.Error(err))
	}

	namespace := os.Getenv("CONTROLLER_NAMESPACE")
	if namespace == "" {
		namespace = "default"
	}

	// Wire up the network manager (used by the reconciler at runtime).
	_ = network.NewNetworkManager(k8sClient, dynamicClient, namespace)

	log.Info("sandbox-controller starting",
		zap.String("namespace", namespace),
	)

	// Wait for shutdown signal.
	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()
	<-ctx.Done()
	log.Info("sandbox-controller shutting down")
}

// loadKubeConfig returns an in-cluster config when running inside a Pod, and
// falls back to the local kubeconfig for development.
func loadKubeConfig() (*rest.Config, error) {
	if cfg, err := rest.InClusterConfig(); err == nil {
		return cfg, nil
	}
	kubeconfig := os.Getenv("KUBECONFIG")
	if kubeconfig == "" {
		if home, err := os.UserHomeDir(); err == nil {
			kubeconfig = home + "/.kube/config"
		}
	}
	return clientcmd.BuildConfigFromFlags("", kubeconfig)
}
