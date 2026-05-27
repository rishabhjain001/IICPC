// Package spawner — network_policy.go builds and applies NetworkPolicy
// objects that enforce the hermetic no-egress requirement for build pods.
//
// Requirements: 2.2 — build containers have no outbound network access.
package spawner

import (
	"context"
	"fmt"

	networkingv1 "k8s.io/api/networking/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes"
)

// BuildNetworkPolicy returns a NetworkPolicy manifest that:
//   - targets pods labelled submission-id=<submissionID> in the build namespace
//   - denies ALL egress (empty egress rule list)
//   - applies only the Egress policy type (ingress is left unrestricted so
//     the init container can pull the source artifact over the cluster-internal
//     network during Task 4.2)
func BuildNetworkPolicy(submissionID, namespace string) *networkingv1.NetworkPolicy {
	return &networkingv1.NetworkPolicy{
		ObjectMeta: metav1.ObjectMeta{
			Name:      fmt.Sprintf("build-no-egress-%s", submissionID),
			Namespace: namespace,
			Labels: map[string]string{
				"app":           "dbhp-build",
				"submission-id": submissionID,
			},
		},
		Spec: networkingv1.NetworkPolicySpec{
			PodSelector: metav1.LabelSelector{
				MatchLabels: map[string]string{
					"submission-id": submissionID,
				},
			},
			PolicyTypes: []networkingv1.PolicyType{
				networkingv1.PolicyTypeEgress,
			},
			// Empty egress slice = deny all outbound traffic.
			Egress: []networkingv1.NetworkPolicyEgressRule{},
		},
	}
}

// ApplyNetworkPolicy creates the no-egress NetworkPolicy for the given
// submission in the Kubernetes API. If the policy already exists the call
// is a no-op (idempotent). Returns an error only when the API call fails for a
// reason other than AlreadyExists.
func ApplyNetworkPolicy(
	ctx context.Context,
	client kubernetes.Interface,
	submissionID, namespace string,
) error {
	policy := BuildNetworkPolicy(submissionID, namespace)
	_, err := client.NetworkingV1().NetworkPolicies(namespace).Create(ctx, policy, metav1.CreateOptions{})
	if err != nil {
		// Treat AlreadyExists as success — policy is already in place.
		if isAlreadyExists(err) {
			return nil
		}
		return fmt.Errorf("spawner: create NetworkPolicy for submission %s: %w", submissionID, err)
	}
	return nil
}

// isAlreadyExists returns true when err is a Kubernetes 409 AlreadyExists
// API error.
func isAlreadyExists(err error) bool {
	type statusCoder interface {
		Status() metav1.Status
	}
	if se, ok := err.(statusCoder); ok {
		return se.Status().Code == 409
	}
	return false
}
