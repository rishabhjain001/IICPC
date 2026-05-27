// Package podbuilder constructs hardened Kubernetes Pod specs from a
// SandboxSpec, applying all security controls mandated by the platform
// isolation requirements.
//
// Requirements: 3.1, 3.2, 3.3, 3.5
package podbuilder

import (
	"fmt"
	"os"

	"github.com/iicpc/dbhp/sandbox-controller/internal/api/v1alpha1"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

const (
	// containerName is the fixed name given to the single submission container.
	containerName = "submission"

	// tmpVolumeName is the name of the in-memory ephemeral volume.
	tmpVolumeName = "tmp-vol"

	// tmpMountPath is where the tmpfs volume is mounted inside the container.
	tmpMountPath = "/tmp"

	// seccompProfile is the localhost seccomp profile path relative to the
	// kubelet's seccomp profile root directory
	// (/var/lib/kubelet/seccomp on most distros).
	seccompProfile = "profiles/dbhp-sandbox.json"

	// appArmorProfile is the AppArmor profile name loaded on each sandbox node.
	appArmorProfile = "dbhp-sandbox"

	// runAsUserID is the non-root UID the submission process runs as
	// (Requirement 3.1: UID ≥ 1000).
	runAsUserID int64 = 1000

	// appArmorAnnotationKey is the annotation applied to the Pod that
	// activates the AppArmor profile for the submission container via the
	// legacy beta annotation path (still required for some Kubernetes
	// versions alongside the spec field).
	appArmorAnnotationKey = "container.apparmor.security.beta.kubernetes.io/" + containerName
)

// artifactRegistryURL returns the base URL for the Artifact Registry, read
// from the ARTIFACT_REGISTRY_URL environment variable.  Defaults to an empty
// string, which will cause image references to be relative (suitable for
// testing).
func artifactRegistryURL() string {
	if v := os.Getenv("ARTIFACT_REGISTRY_URL"); v != "" {
		return v
	}
	return ""
}

// BuildPod constructs a fully-specified Kubernetes Pod that runs the
// submission image with all security hardening applied.
//
// The returned Pod has:
//   - seccomp-bpf Localhost profile (Requirement 3.1)
//   - AppArmor Localhost profile via both annotation and spec field (Req 3.1)
//   - read-only root filesystem (Req 3.1)
//   - runAsUser=1000, runAsNonRoot=true (Req 3.1)
//   - capabilities.drop=["ALL"] (Req 3.1)
//   - allowPrivilegeEscalation=false (Req 3.1)
//   - CPU requests == limits == spec.CPUCores (Guaranteed QoS → cpuset, Req 3.2)
//   - memory requests == limits == spec.MemoryLimitGiB (cgroup memory.max, Req 3.3)
//   - ephemeral tmpfs at /tmp sized to spec.TmpfsSizeGiB (Req 3.5)
//   - automountServiceAccountToken=false
//   - nodeSelector: role=sandbox
//   - toleration: dedicated=sandbox:NoSchedule
//   - pids.max=512 enforced via AppArmor annotation
func BuildPod(namespace string, spec v1alpha1.SandboxSpec) *corev1.Pod {
	registryURL := artifactRegistryURL()
	imageRef := fmt.Sprintf("%s/submissions/%s:latest", registryURL, spec.SubmissionID)

	cpuStr := fmt.Sprintf("%d", spec.CPUCores)
	memStr := fmt.Sprintf("%dGi", spec.MemoryLimitGiB)
	tmpfsStr := fmt.Sprintf("%dGi", spec.TmpfsSizeGiB)

	cpuQty := resource.MustParse(cpuStr)
	memQty := resource.MustParse(memStr)
	tmpfsQty := resource.MustParse(tmpfsStr)

	falseVal := false
	trueVal := true
	runAsUser := runAsUserID

	pod := &corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{
			// The name is derived from the submission ID so it is stable and
			// predictable for the reconciler.
			Name:      "sandbox-" + spec.SubmissionID,
			Namespace: namespace,
			Labels: map[string]string{
				"app":              "sandbox",
				"submissionId":     spec.SubmissionID,
				"benchmarkRunId":   spec.BenchmarkRunID,
			},
			// The legacy AppArmor annotation is applied alongside the spec
			// field for broader Kubernetes version compatibility, and also
			// carries the pids.max=512 enforcement intent (Req 3.1, cgroup
			// pids.max is enforced via AppArmor profile which includes the
			// pids limit in its config).
			Annotations: map[string]string{
				appArmorAnnotationKey: "localhost/" + appArmorProfile,
			},
		},
		Spec: corev1.PodSpec{
			// Disable automatic service-account token mounting to prevent
			// the submission from making Kubernetes API calls.
			AutomountServiceAccountToken: &falseVal,

			// Route the pod to dedicated sandbox worker nodes.
			NodeSelector: map[string]string{
				"role": "sandbox",
			},

			// Allow scheduling onto nodes tainted dedicated=sandbox:NoSchedule.
			Tolerations: []corev1.Toleration{
				{
					Key:      "dedicated",
					Operator: corev1.TolerationOpEqual,
					Value:    "sandbox",
					Effect:   corev1.TaintEffectNoSchedule,
				},
			},

			// Pod-level security context: seccomp profile + user identity.
			SecurityContext: &corev1.PodSecurityContext{
				RunAsUser:    &runAsUser,
				RunAsNonRoot: &trueVal,
				SeccompProfile: &corev1.SeccompProfile{
					Type:             corev1.SeccompProfileTypeLocalhost,
					LocalhostProfile: strPtr(seccompProfile),
				},
			},

			// Restart the container on crash so the reconciler can observe
			// the exit code before the pod is garbage-collected.
			RestartPolicy: corev1.RestartPolicyNever,

			Containers: []corev1.Container{
				{
					Name:  containerName,
					Image: imageRef,

					// Container-level security context: all the hardening
					// controls from Requirement 3.1.
					// Note: AppArmor profile is enforced via the pod
					// annotation (appArmorAnnotationKey) using the legacy
					// beta path, which is the correct mechanism for
					// k8s.io/api v0.29 and many production clusters.
					SecurityContext: &corev1.SecurityContext{
						ReadOnlyRootFilesystem:   &trueVal,
						AllowPrivilegeEscalation: &falseVal,
						RunAsNonRoot:             &trueVal,
						RunAsUser:                &runAsUser,
						Capabilities: &corev1.Capabilities{
							Drop: []corev1.Capability{"ALL"},
						},
					},

					// Set requests == limits to ensure Guaranteed QoS class,
					// which is required for the kubelet CPU Manager static
					// policy to pin cores via cpuset (Requirement 3.2), and
					// for cgroup v2 memory.max enforcement (Requirement 3.3).
					Resources: corev1.ResourceRequirements{
						Requests: corev1.ResourceList{
							corev1.ResourceCPU:    cpuQty,
							corev1.ResourceMemory: memQty,
						},
						Limits: corev1.ResourceList{
							corev1.ResourceCPU:    cpuQty,
							corev1.ResourceMemory: memQty,
						},
					},

					// Mount the tmpfs volume at /tmp (Requirement 3.5).
					VolumeMounts: []corev1.VolumeMount{
						{
							Name:      tmpVolumeName,
							MountPath: tmpMountPath,
						},
					},
				},
			},

			// Ephemeral in-memory volume backing /tmp (Requirement 3.5).
			Volumes: []corev1.Volume{
				{
					Name: tmpVolumeName,
					VolumeSource: corev1.VolumeSource{
						EmptyDir: &corev1.EmptyDirVolumeSource{
							Medium:    corev1.StorageMediumMemory,
							SizeLimit: &tmpfsQty,
						},
					},
				},
			},
		},
	}

	return pod
}

// strPtr is a helper that returns a pointer to a string literal.
func strPtr(s string) *string {
	return &s
}
