// Package network manages per-benchmark-run isolated overlay networks using
// Multus CNI NetworkAttachmentDefinitions and Kubernetes NetworkPolicies.
//
// Requirements: 3.4
package network

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"

	corev1 "k8s.io/api/core/v1"
	networkingv1 "k8s.io/api/networking/v1"
	k8serrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/client-go/dynamic"
	"k8s.io/client-go/kubernetes"
)

// ErrOverlayCreationFailed is returned by CreateOverlay when the Kubernetes
// API call to create either the NetworkAttachmentDefinition or the
// NetworkPolicy fails.  Callers that receive this error must set the Benchmark
// Run status to NETWORK_SETUP_FAILED (Requirement 3.4).
var ErrOverlayCreationFailed = errors.New("overlay network creation failed")

// nadGVR is the GroupVersionResource for Multus NetworkAttachmentDefinitions.
var nadGVR = schema.GroupVersionResource{
	Group:    "k8s.cni.cncf.io",
	Version:  "v1",
	Resource: "network-attachment-definitions",
}

// OverlayNetwork describes the per-run isolated overlay created by
// NetworkManager.CreateOverlay.
type OverlayNetwork struct {
	// Name is the Kubernetes resource name of the NetworkAttachmentDefinition,
	// e.g. "sandbox-run-<benchmarkRunID>".
	Name string

	// BotCIDR is the CIDR from which BotFleetManager pods are allowed to
	// send ingress traffic to sandboxes on this overlay.
	BotCIDR string

	// TelemetryCIDR is the CIDR from which TelemetryIngester pods are
	// allowed to send ingress traffic to sandboxes on this overlay.
	TelemetryCIDR string
}

// NetworkManager creates and destroys per-run CNI overlay networks.
type NetworkManager interface {
	// CreateOverlay provisions a Multus macvlan NetworkAttachmentDefinition
	// and a NetworkPolicy for the given benchmarkRunID.  On failure it
	// returns a wrapped ErrOverlayCreationFailed.
	CreateOverlay(ctx context.Context, benchmarkRunID string) (*OverlayNetwork, error)

	// DeleteOverlay removes the NetworkAttachmentDefinition and NetworkPolicy
	// created by CreateOverlay for the given benchmarkRunID.
	DeleteOverlay(ctx context.Context, benchmarkRunID string) error
}

// kubeNetworkManager is the production implementation of NetworkManager that
// talks to the Kubernetes API.
type kubeNetworkManager struct {
	client        kubernetes.Interface
	dynamicClient dynamic.Interface
	namespace     string
}

// NewNetworkManager creates a NetworkManager backed by the given Kubernetes
// clients.
func NewNetworkManager(client kubernetes.Interface, dynamicClient dynamic.Interface, namespace string) NetworkManager {
	return &kubeNetworkManager{
		client:        client,
		dynamicClient: dynamicClient,
		namespace:     namespace,
	}
}

// nadName returns the resource name used for both the NAD and the
// NetworkPolicy for a given run.
func nadName(benchmarkRunID string) string {
	return "sandbox-run-" + benchmarkRunID
}

// macvlanConfig is the CNI configuration embedded in the
// NetworkAttachmentDefinition.
type macvlanConfig struct {
	CniVersion string      `json:"cniVersion"`
	Type       string      `json:"type"`
	Master     string      `json:"master"`
	Mode       string      `json:"mode"`
	IPAM       macvlanIPAM `json:"ipam"`
}

type macvlanIPAM struct {
	Type  string `json:"type"`
	Range string `json:"range"`
}

// CreateOverlay provisions a Multus macvlan NetworkAttachmentDefinition and a
// restrictive NetworkPolicy for the given benchmarkRunID.
//
// The NetworkPolicy permits ingress only from the BotFleetManager CIDR
// (BOT_FLEET_CIDR env var) and the TelemetryIngester CIDR
// (TELEMETRY_INGESTER_CIDR env var).
//
// On any Kubernetes API error, the method returns a wrapped
// ErrOverlayCreationFailed so the reconciler can set the run status to
// NETWORK_SETUP_FAILED (Requirement 3.4).
func (m *kubeNetworkManager) CreateOverlay(ctx context.Context, benchmarkRunID string) (*OverlayNetwork, error) {
	botCIDR := os.Getenv("BOT_FLEET_CIDR")
	if botCIDR == "" {
		botCIDR = "10.0.0.0/24" // safe default for dev; override in production
	}
	telemetryCIDR := os.Getenv("TELEMETRY_INGESTER_CIDR")
	if telemetryCIDR == "" {
		telemetryCIDR = "10.0.1.0/24"
	}

	name := nadName(benchmarkRunID)

	// --- 1. Create the Multus NetworkAttachmentDefinition ----------------

	// Use a deterministic /24 subnet derived from the run index.  In
	// production a proper IPAM (e.g. Whereabouts) manages the ranges; here
	// we encode the intent so the NAD is well-formed.
	cniCfg := macvlanConfig{
		CniVersion: "0.3.1",
		Type:       "macvlan",
		Master:     "eth1",
		Mode:       "bridge",
		IPAM: macvlanIPAM{
			Type:  "whereabouts",
			Range: fmt.Sprintf("10.100.0.0/24"), // whereabouts assigns per-pod IPs
		},
	}
	cniCfgJSON, err := json.Marshal(cniCfg)
	if err != nil {
		return nil, fmt.Errorf("%w: marshal CNI config: %v", ErrOverlayCreationFailed, err)
	}

	nad := &unstructured.Unstructured{
		Object: map[string]interface{}{
			"apiVersion": "k8s.cni.cncf.io/v1",
			"kind":       "NetworkAttachmentDefinition",
			"metadata": map[string]interface{}{
				"name":      name,
				"namespace": m.namespace,
				"labels": map[string]interface{}{
					"benchmarkRunId": benchmarkRunID,
				},
			},
			"spec": map[string]interface{}{
				"config": string(cniCfgJSON),
			},
		},
	}

	_, err = m.dynamicClient.Resource(nadGVR).Namespace(m.namespace).Create(ctx, nad, metav1.CreateOptions{})
	if err != nil && !k8serrors.IsAlreadyExists(err) {
		return nil, fmt.Errorf("%w: create NetworkAttachmentDefinition %q: %v", ErrOverlayCreationFailed, name, err)
	}

	// --- 2. Create the NetworkPolicy ------------------------------------

	networkPolicy := buildNetworkPolicy(name, m.namespace, benchmarkRunID, botCIDR, telemetryCIDR)
	_, err = m.client.NetworkingV1().NetworkPolicies(m.namespace).Create(ctx, networkPolicy, metav1.CreateOptions{})
	if err != nil && !k8serrors.IsAlreadyExists(err) {
		// Best-effort cleanup of the NAD we just created.
		_ = m.dynamicClient.Resource(nadGVR).Namespace(m.namespace).Delete(ctx, name, metav1.DeleteOptions{})
		return nil, fmt.Errorf("%w: create NetworkPolicy %q: %v", ErrOverlayCreationFailed, name, err)
	}

	return &OverlayNetwork{
		Name:          name,
		BotCIDR:       botCIDR,
		TelemetryCIDR: telemetryCIDR,
	}, nil
}

// buildNetworkPolicy returns a NetworkPolicy that restricts ingress to
// sandbox pods to traffic from the BotFleetManager and TelemetryIngester
// CIDRs only.
func buildNetworkPolicy(name, namespace, benchmarkRunID, botCIDR, telemetryCIDR string) *networkingv1.NetworkPolicy {
	protoTCP := corev1.ProtocolTCP
	return &networkingv1.NetworkPolicy{
		ObjectMeta: metav1.ObjectMeta{
			Name:      name,
			Namespace: namespace,
			Labels: map[string]string{
				"benchmarkRunId": benchmarkRunID,
			},
		},
		Spec: networkingv1.NetworkPolicySpec{
			// Select all sandbox pods that belong to this run.
			PodSelector: metav1.LabelSelector{
				MatchLabels: map[string]string{
					"benchmarkRunId": benchmarkRunID,
				},
			},
			PolicyTypes: []networkingv1.PolicyType{
				networkingv1.PolicyTypeIngress,
			},
			Ingress: []networkingv1.NetworkPolicyIngressRule{
				// Allow from BotFleetManager CIDR.
				{
					From: []networkingv1.NetworkPolicyPeer{
						{
							IPBlock: &networkingv1.IPBlock{
								CIDR: botCIDR,
							},
						},
					},
					Ports: allTCPPorts(protoTCP),
				},
				// Allow from TelemetryIngester CIDR.
				{
					From: []networkingv1.NetworkPolicyPeer{
						{
							IPBlock: &networkingv1.IPBlock{
								CIDR: telemetryCIDR,
							},
						},
					},
					Ports: allTCPPorts(protoTCP),
				},
			},
		},
	}
}

// allTCPPorts returns a NetworkPolicyPort slice that matches all TCP ports.
// The Protocol field in networkingv1.NetworkPolicyPort is *corev1.Protocol
// (not networkingv1.Protocol which does not exist in the k8s.io/api v0.29 package).
func allTCPPorts(proto corev1.Protocol) []networkingv1.NetworkPolicyPort {
	// An empty port range means all ports in Kubernetes NetworkPolicy.
	return []networkingv1.NetworkPolicyPort{
		{Protocol: &proto},
	}
}

// DeleteOverlay removes the NetworkAttachmentDefinition and NetworkPolicy
// that were created by CreateOverlay for the given benchmarkRunID.
//
// Both deletions are attempted even if the first one fails; errors from both
// are combined and returned.
func (m *kubeNetworkManager) DeleteOverlay(ctx context.Context, benchmarkRunID string) error {
	name := nadName(benchmarkRunID)
	var errs []error

	err := m.dynamicClient.Resource(nadGVR).Namespace(m.namespace).Delete(ctx, name, metav1.DeleteOptions{})
	if err != nil && !k8serrors.IsNotFound(err) {
		errs = append(errs, fmt.Errorf("delete NetworkAttachmentDefinition %q: %w", name, err))
	}

	err = m.client.NetworkingV1().NetworkPolicies(m.namespace).Delete(ctx, name, metav1.DeleteOptions{})
	if err != nil && !k8serrors.IsNotFound(err) {
		errs = append(errs, fmt.Errorf("delete NetworkPolicy %q: %w", name, err))
	}

	if len(errs) > 0 {
		return errors.Join(errs...)
	}
	return nil
}


