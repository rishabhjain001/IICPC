// overlay_test.go verifies NetworkManager behaviour using Kubernetes fake
// clients, avoiding any real cluster dependency.
//
// Requirements: 3.4
package network_test

import (
	"context"
	"errors"
	"testing"
	"time"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	dynamicfake "k8s.io/client-go/dynamic/fake"
	"k8s.io/client-go/kubernetes/fake"
	k8stesting "k8s.io/client-go/testing"

	"github.com/iicpc/dbhp/sandbox-controller/internal/network"
)

// nadGVR matches the GVR used inside the overlay package.
var nadGVR = schema.GroupVersionResource{
	Group:    "k8s.cni.cncf.io",
	Version:  "v1",
	Resource: "network-attachment-definitions",
}

// newFakeClients builds a fake kubernetes client and a fake dynamic client
// that are pre-registered with the NAD GVR so dynamic operations succeed.
func newFakeClients(scheme *runtime.Scheme) (*fake.Clientset, *dynamicfake.FakeDynamicClient) {
	// Register the NAD GVR in the scheme so the fake dynamic client can
	// handle it without returning unknown-resource errors.
	scheme.AddKnownTypeWithName(
		schema.GroupVersionKind{Group: "k8s.cni.cncf.io", Version: "v1", Kind: "NetworkAttachmentDefinition"},
		&runtime.Unknown{},
	)
	scheme.AddKnownTypeWithName(
		schema.GroupVersionKind{Group: "k8s.cni.cncf.io", Version: "v1", Kind: "NetworkAttachmentDefinitionList"},
		&runtime.Unknown{},
	)

	k8sClient := fake.NewSimpleClientset()
	dynamicClient := dynamicfake.NewSimpleDynamicClientWithCustomListKinds(
		scheme,
		map[schema.GroupVersionResource]string{
			nadGVR: "NetworkAttachmentDefinitionList",
		},
	)
	return k8sClient, dynamicClient
}

// ---------------------------------------------------------------------------
// TestCreateOverlay_Success
// ---------------------------------------------------------------------------

// TestCreateOverlay_Success verifies that a successful call to CreateOverlay
// returns an OverlayNetwork with a non-empty Name (Requirement 3.4).
func TestCreateOverlay_Success(t *testing.T) {
	scheme := runtime.NewScheme()
	k8sClient, dynamicClient := newFakeClients(scheme)

	nm := network.NewNetworkManager(k8sClient, dynamicClient, "default")

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	overlay, err := nm.CreateOverlay(ctx, "run-success-001")
	if err != nil {
		t.Fatalf("CreateOverlay: unexpected error: %v", err)
	}
	if overlay == nil {
		t.Fatal("CreateOverlay returned nil OverlayNetwork")
	}
	if overlay.Name == "" {
		t.Error("OverlayNetwork.Name must not be empty")
	}
	// The name should encode the run ID for traceability.
	const runID = "run-success-001"
	found := false
	for i := 0; i <= len(overlay.Name)-len(runID); i++ {
		if overlay.Name[i:i+len(runID)] == runID {
			found = true
			break
		}
	}
	if !found {
		t.Errorf("OverlayNetwork.Name %q does not contain benchmarkRunID %q", overlay.Name, runID)
	}
}

// TestCreateOverlay_StoresNetworkName verifies that after a successful
// CreateOverlay the returned network name is stable across the same run ID
// (deterministic naming) (Requirement 3.4).
func TestCreateOverlay_StoresNetworkName(t *testing.T) {
	scheme := runtime.NewScheme()
	k8sClient, dynamicClient := newFakeClients(scheme)

	nm := network.NewNetworkManager(k8sClient, dynamicClient, "default")

	ctx := context.Background()
	overlay, err := nm.CreateOverlay(ctx, "run-store-test")
	if err != nil {
		t.Fatalf("CreateOverlay: %v", err)
	}

	// The name should be deterministically derived from the run ID.
	expected := "sandbox-run-run-store-test"
	if overlay.Name != expected {
		t.Errorf("expected overlay name %q, got %q", expected, overlay.Name)
	}
}

// ---------------------------------------------------------------------------
// TestCreateOverlay_ErrOverlayCreationFailed_KubernetesUnavailable
// ---------------------------------------------------------------------------

// TestCreateOverlay_ErrOverlayCreationFailed_KubernetesUnavailable verifies
// that when the Kubernetes API is unavailable (simulated by a reactor that
// returns an error), CreateOverlay returns a wrapped ErrOverlayCreationFailed
// (Requirement 3.4).
func TestCreateOverlay_ErrOverlayCreationFailed_KubernetesUnavailable(t *testing.T) {
	scheme := runtime.NewScheme()
	k8sClient, dynamicClient := newFakeClients(scheme)

	// Inject a fault: the dynamic client returns an error for every Create
	// call, simulating a Kubernetes API server being unavailable.
	dynamicClient.Fake.PrependReactor("create", "network-attachment-definitions",
		func(_ k8stesting.Action) (bool, runtime.Object, error) {
			return true, nil, errors.New("connection refused: simulated API unavailable")
		},
	)

	nm := network.NewNetworkManager(k8sClient, dynamicClient, "default")

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	_, err := nm.CreateOverlay(ctx, "run-fail-001")
	if err == nil {
		t.Fatal("expected ErrOverlayCreationFailed, got nil")
	}
	if !errors.Is(err, network.ErrOverlayCreationFailed) {
		t.Errorf("expected wrapped ErrOverlayCreationFailed, got: %v", err)
	}
}

// TestCreateOverlay_ErrOverlayCreationFailed_NetworkPolicyFails verifies that
// when the NAD creation succeeds but NetworkPolicy creation fails, CreateOverlay
// still returns ErrOverlayCreationFailed (Requirement 3.4).
func TestCreateOverlay_ErrOverlayCreationFailed_NetworkPolicyFails(t *testing.T) {
	scheme := runtime.NewScheme()
	k8sClient, dynamicClient := newFakeClients(scheme)

	// Inject a fault on NetworkPolicy creation only.
	k8sClient.Fake.PrependReactor("create", "networkpolicies",
		func(_ k8stesting.Action) (bool, runtime.Object, error) {
			return true, nil, errors.New("connection refused: simulated API unavailable")
		},
	)

	nm := network.NewNetworkManager(k8sClient, dynamicClient, "default")

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	_, err := nm.CreateOverlay(ctx, "run-netpol-fail")
	if err == nil {
		t.Fatal("expected ErrOverlayCreationFailed, got nil")
	}
	if !errors.Is(err, network.ErrOverlayCreationFailed) {
		t.Errorf("expected wrapped ErrOverlayCreationFailed, got: %v", err)
	}
}

// ---------------------------------------------------------------------------
// TestDeleteOverlay
// ---------------------------------------------------------------------------

// TestDeleteOverlay_SuccessfulCleanup verifies that DeleteOverlay removes both
// the NetworkAttachmentDefinition and the NetworkPolicy without error when they
// both exist.
func TestDeleteOverlay_SuccessfulCleanup(t *testing.T) {
	scheme := runtime.NewScheme()
	k8sClient, dynamicClient := newFakeClients(scheme)

	nm := network.NewNetworkManager(k8sClient, dynamicClient, "default")
	ctx := context.Background()

	// Create first, then delete.
	if _, err := nm.CreateOverlay(ctx, "run-del-001"); err != nil {
		t.Fatalf("CreateOverlay setup: %v", err)
	}
	if err := nm.DeleteOverlay(ctx, "run-del-001"); err != nil {
		t.Fatalf("DeleteOverlay: unexpected error: %v", err)
	}
}

// TestDeleteOverlay_IdempotentWhenNotFound verifies that DeleteOverlay does
// not return an error if the resources have already been deleted.
func TestDeleteOverlay_IdempotentWhenNotFound(t *testing.T) {
	scheme := runtime.NewScheme()
	k8sClient, dynamicClient := newFakeClients(scheme)

	nm := network.NewNetworkManager(k8sClient, dynamicClient, "default")
	ctx := context.Background()

	// Delete a run that was never created — should be a no-op.
	if err := nm.DeleteOverlay(ctx, "run-never-existed"); err != nil {
		t.Errorf("DeleteOverlay on non-existent resources should be a no-op, got: %v", err)
	}
}

// ---------------------------------------------------------------------------
// TestCreateOverlay_BotAndTelemetryCIDRPopulated
// ---------------------------------------------------------------------------

// TestCreateOverlay_BotAndTelemetryCIDRPopulated verifies that the returned
// OverlayNetwork carries non-empty CIDR fields for BotFleetManager and
// TelemetryIngester (Requirement 3.4).
func TestCreateOverlay_BotAndTelemetryCIDRPopulated(t *testing.T) {
	scheme := runtime.NewScheme()
	k8sClient, dynamicClient := newFakeClients(scheme)

	t.Setenv("BOT_FLEET_CIDR", "10.10.0.0/24")
	t.Setenv("TELEMETRY_INGESTER_CIDR", "10.10.1.0/24")

	nm := network.NewNetworkManager(k8sClient, dynamicClient, "default")
	ctx := context.Background()

	overlay, err := nm.CreateOverlay(ctx, "run-cidr-check")
	if err != nil {
		t.Fatalf("CreateOverlay: %v", err)
	}
	if overlay.BotCIDR == "" {
		t.Error("OverlayNetwork.BotCIDR must not be empty")
	}
	if overlay.TelemetryCIDR == "" {
		t.Error("OverlayNetwork.TelemetryCIDR must not be empty")
	}
	if overlay.BotCIDR != "10.10.0.0/24" {
		t.Errorf("BotCIDR: expected 10.10.0.0/24, got %q", overlay.BotCIDR)
	}
	if overlay.TelemetryCIDR != "10.10.1.0/24" {
		t.Errorf("TelemetryCIDR: expected 10.10.1.0/24, got %q", overlay.TelemetryCIDR)
	}
}

// ensure the metav1 import is used
var _ = metav1.Now
