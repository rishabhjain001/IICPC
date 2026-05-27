package spawner_test

import (
	"context"
	"testing"

	networkingv1 "k8s.io/api/networking/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes/fake"

	"github.com/iicpc/dbhp/build-pipeline/internal/spawner"
)

// ---------------------------------------------------------------------------
// BuildNetworkPolicy tests
// ---------------------------------------------------------------------------

// TestBuildNetworkPolicy_DeniesAllEgress verifies that the produced
// NetworkPolicy has an empty egress rule list (deny-all).
func TestBuildNetworkPolicy_DeniesAllEgress(t *testing.T) {
	t.Parallel()

	submissionID := "sub-np-001"
	policy := spawner.ExportBuildNetworkPolicy(submissionID, testNamespace)

	if len(policy.Spec.Egress) != 0 {
		t.Errorf("expected empty egress rules (deny-all), got %d rules", len(policy.Spec.Egress))
	}
}

// TestBuildNetworkPolicy_PolicyTypeIsEgressOnly verifies that only the Egress
// policy type is declared (ingress traffic is left unrestricted so the init
// container can pull the artifact).
func TestBuildNetworkPolicy_PolicyTypeIsEgressOnly(t *testing.T) {
	t.Parallel()

	policy := spawner.ExportBuildNetworkPolicy("sub-np-002", testNamespace)

	if len(policy.Spec.PolicyTypes) != 1 {
		t.Fatalf("expected exactly 1 policy type, got %d", len(policy.Spec.PolicyTypes))
	}
	if policy.Spec.PolicyTypes[0] != networkingv1.PolicyTypeEgress {
		t.Errorf("expected policy type Egress, got %v", policy.Spec.PolicyTypes[0])
	}
}

// TestBuildNetworkPolicy_PodSelectorMatchesSubmissionID verifies that the
// NetworkPolicy targets only pods labelled with the given submission ID.
func TestBuildNetworkPolicy_PodSelectorMatchesSubmissionID(t *testing.T) {
	t.Parallel()

	submissionID := "sub-np-003"
	policy := spawner.ExportBuildNetworkPolicy(submissionID, testNamespace)

	labels := policy.Spec.PodSelector.MatchLabels
	if labels["submission-id"] != submissionID {
		t.Errorf("podSelector.matchLabels[submission-id] = %q, want %q",
			labels["submission-id"], submissionID)
	}
}

// TestBuildNetworkPolicy_NameFollowsConvention verifies the policy name.
func TestBuildNetworkPolicy_NameFollowsConvention(t *testing.T) {
	t.Parallel()

	submissionID := "sub-np-004"
	policy := spawner.ExportBuildNetworkPolicy(submissionID, testNamespace)

	want := "build-no-egress-" + submissionID
	if policy.Name != want {
		t.Errorf("NetworkPolicy name = %q, want %q", policy.Name, want)
	}
}

// TestBuildNetworkPolicy_NamespaceIsSet verifies the policy is in the right
// namespace.
func TestBuildNetworkPolicy_NamespaceIsSet(t *testing.T) {
	t.Parallel()

	policy := spawner.ExportBuildNetworkPolicy("sub-np-005", testNamespace)

	if policy.Namespace != testNamespace {
		t.Errorf("NetworkPolicy namespace = %q, want %q", policy.Namespace, testNamespace)
	}
}

// ---------------------------------------------------------------------------
// ApplyNetworkPolicy tests
// ---------------------------------------------------------------------------

// TestApplyNetworkPolicy_CreatesPolicy verifies that ApplyNetworkPolicy calls
// the Kubernetes API and the policy is retrievable afterwards.
func TestApplyNetworkPolicy_CreatesPolicy(t *testing.T) {
	t.Parallel()

	fakeClient := fake.NewSimpleClientset()
	submissionID := "sub-np-apply-001"

	err := spawner.ExportApplyNetworkPolicy(context.Background(), fakeClient, submissionID, testNamespace)
	if err != nil {
		t.Fatalf("ApplyNetworkPolicy returned unexpected error: %v", err)
	}

	// Confirm the policy exists in the fake client.
	policyName := "build-no-egress-" + submissionID
	policy, err := fakeClient.NetworkingV1().NetworkPolicies(testNamespace).Get(
		context.Background(), policyName, metav1.GetOptions{},
	)
	if err != nil {
		t.Fatalf("expected NetworkPolicy %q to exist: %v", policyName, err)
	}
	if len(policy.Spec.Egress) != 0 {
		t.Errorf("policy should have empty egress, got %d rules", len(policy.Spec.Egress))
	}
}

// TestApplyNetworkPolicy_IdempotentOnAlreadyExists verifies that calling
// ApplyNetworkPolicy twice does not return an error on the second call.
func TestApplyNetworkPolicy_IdempotentOnAlreadyExists(t *testing.T) {
	t.Parallel()

	fakeClient := fake.NewSimpleClientset()
	submissionID := "sub-np-apply-002"

	// First call — should succeed.
	if err := spawner.ExportApplyNetworkPolicy(context.Background(), fakeClient, submissionID, testNamespace); err != nil {
		t.Fatalf("first ApplyNetworkPolicy call failed: %v", err)
	}

	// Second call — should be silently ignored (AlreadyExists).
	if err := spawner.ExportApplyNetworkPolicy(context.Background(), fakeClient, submissionID, testNamespace); err != nil {
		t.Fatalf("second ApplyNetworkPolicy call (idempotent) returned error: %v", err)
	}
}

// ensure networkingv1 is used (compile-time guard).
var _ = networkingv1.NetworkPolicy{}

// ensure metav1 is used (compile-time guard).
var _ = metav1.GetOptions{}
