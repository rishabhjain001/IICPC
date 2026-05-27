// Package main is the entry point for the Bot Fleet Manager service.
package main

import (
	"context"
	"os"
	"os/signal"
	"syscall"

	"go.uber.org/zap"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/rest"

	grpcserver "github.com/iicpc/dbhp/bot-fleet-manager/internal/grpc"
	"github.com/iicpc/dbhp/bot-fleet-manager/internal/heartbeat"
	"github.com/iicpc/dbhp/bot-fleet-manager/internal/store"
)

func main() {
	logger, err := zap.NewProduction()
	if err != nil {
		panic("failed to create logger: " + err.Error())
	}
	defer logger.Sync() //nolint:errcheck

	// Build in-cluster Kubernetes client.
	cfg, err := rest.InClusterConfig()
	if err != nil {
		logger.Fatal("failed to get in-cluster config", zap.Error(err))
	}
	k8sClient, err := kubernetes.NewForConfig(cfg)
	if err != nil {
		logger.Fatal("failed to create k8s client", zap.Error(err))
	}

	// Initialise shared state store.
	fleetStore := store.NewFleetStore()

	// Start heartbeat monitor.
	monitor := heartbeat.NewMonitor(logger)
	ctx, cancel := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer cancel()

	go monitor.Run(ctx, func(nodeName string) {
		logger.Warn("worker node failure detected; redistribution required",
			zap.String("node", nodeName),
		)
		// In a full implementation this would trigger bot redistribution.
	})

	// Build gRPC server (listener and actual gRPC binding would be wired here
	// in a production deployment; omitted because protoc-generated stubs are
	// not available).
	srv := grpcserver.NewServer(k8sClient, logger, fleetStore)
	_ = srv // Used in production gRPC handler registration.

	logger.Info("bot-fleet-manager started")
	<-ctx.Done()
	logger.Info("bot-fleet-manager shutting down")
}
