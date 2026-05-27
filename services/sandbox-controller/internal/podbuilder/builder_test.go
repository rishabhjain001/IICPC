// builder_test.go verifies that BuildPod produces a Pod spec that satisfies
// all security hardening requirements.
//
// Requirements: 3.1, 3.2, 3.3, 3.5
package podbuilder_test

import (
	"testing"

	corev1 "k8s.io/api/core/v1"

	"github.com/iicpc/dbhp/sandbox-controller/internal/api/v1alpha1"
	"github.com/iicpc/dbhp/sandbox-controller/internal/podbuilder"
)

// defaultSpec returns a SandboxSpec with typical benchmark values.
func defaultSpec() v1alpha1.SandboxSpec {
	return v1alpha1.SandboxSpec{
		SubmissionID:       "sub-abc-123",
		BenchmarkRunID:     "run-xyz-456",
		CPUCores:           4,
		MemoryLimitGiB:     4,
		TmpfsSizeGiB:       1,
		MaxLifetimeSeconds: 7200,
		HealthCheckPath:    "/health",
		Protocols:          []string{"FIX", "REST", "WS"},
	}
}

// submissionContainer returns the first container from the pod spec, failing
// the test if there is none.
func submissionContainer(t *testing.T, pod *corev1.Pod) corev1.Container {
	t.Helper()
	if len(pod.Spec.Containers) == 0 {
		t.Fatal("pod has no containers")
	}
	return pod.Spec.Containers[0]
}

// ---------------------------------------------------------------------------
// Requirement 3.1 — security context hardening
// ---------------------------------------------------------------------------

// TestBuildPod_ReadOnlyRootFilesystem verifies that the container's root
// filesystem is set to read-only (Requirement 3.1).
func TestBuildPod_ReadOnlyRootFilesystem(t *testing.T) {
	pod := podbuilder.BuildPod("default", defaultSpec())
	c := submissionContainer(t, pod)

	if c.SecurityContext == nil {
		t.Fatal("container SecurityContext is nil")
	}
	if c.SecurityContext.ReadOnlyRootFilesystem == nil {
		t.Fatal("ReadOnlyRootFilesystem is nil, expected true")
	}
	if !*c.SecurityContext.ReadOnlyRootFilesystem {
		t.Error("ReadOnlyRootFilesystem must be true (Requirement 3.1)")
	}
}

// TestBuildPod_RunAsNonRoot verifies that runAsNonRoot is true at both the
// pod and container level (Requirement 3.1).
func TestBuildPod_RunAsNonRoot(t *testing.T) {
	pod := podbuilder.BuildPod("default", defaultSpec())
	c := submissionContainer(t, pod)

	// Pod-level.
	if pod.Spec.SecurityContext == nil {
		t.Fatal("pod SecurityContext is nil")
	}
	if pod.Spec.SecurityContext.RunAsNonRoot == nil || !*pod.Spec.SecurityContext.RunAsNonRoot {
		t.Error("pod-level RunAsNonRoot must be true (Requirement 3.1)")
	}

	// Container-level.
	if c.SecurityContext == nil {
		t.Fatal("container SecurityContext is nil")
	}
	if c.SecurityContext.RunAsNonRoot == nil || !*c.SecurityContext.RunAsNonRoot {
		t.Error("container-level RunAsNonRoot must be true (Requirement 3.1)")
	}
}

// TestBuildPod_RunAsUser1000 verifies that the UID is set to 1000 at both
// the pod and container level (Requirement 3.1: UID ≥ 1000).
func TestBuildPod_RunAsUser1000(t *testing.T) {
	const expectedUID int64 = 1000

	pod := podbuilder.BuildPod("default", defaultSpec())
	c := submissionContainer(t, pod)

	if pod.Spec.SecurityContext.RunAsUser == nil {
		t.Fatal("pod-level RunAsUser is nil")
	}
	if *pod.Spec.SecurityContext.RunAsUser != expectedUID {
		t.Errorf("pod-level RunAsUser: expected %d, got %d", expectedUID, *pod.Spec.SecurityContext.RunAsUser)
	}

	if c.SecurityContext.RunAsUser == nil {
		t.Fatal("container-level RunAsUser is nil")
	}
	if *c.SecurityContext.RunAsUser != expectedUID {
		t.Errorf("container-level RunAsUser: expected %d, got %d", expectedUID, *c.SecurityContext.RunAsUser)
	}
}

// TestBuildPod_CapabilitiesDropAll verifies that capabilities.drop = ["ALL"]
// is set on the container (Requirement 3.1).
func TestBuildPod_CapabilitiesDropAll(t *testing.T) {
	pod := podbuilder.BuildPod("default", defaultSpec())
	c := submissionContainer(t, pod)

	if c.SecurityContext == nil || c.SecurityContext.Capabilities == nil {
		t.Fatal("container SecurityContext.Capabilities is nil")
	}
	dropped := c.SecurityContext.Capabilities.Drop
	if len(dropped) != 1 {
		t.Fatalf("expected exactly 1 dropped capability (ALL), got %v", dropped)
	}
	if dropped[0] != "ALL" {
		t.Errorf("expected capabilities.drop[0] = ALL, got %q", dropped[0])
	}
	// No capabilities should be added.
	if len(c.SecurityContext.Capabilities.Add) != 0 {
		t.Errorf("expected no added capabilities, got %v", c.SecurityContext.Capabilities.Add)
	}
}

// TestBuildPod_AllowPrivilegeEscalationFalse verifies that privilege
// escalation is disabled (Requirement 3.1).
func TestBuildPod_AllowPrivilegeEscalationFalse(t *testing.T) {
	pod := podbuilder.BuildPod("default", defaultSpec())
	c := submissionContainer(t, pod)

	if c.SecurityContext == nil {
		t.Fatal("container SecurityContext is nil")
	}
	if c.SecurityContext.AllowPrivilegeEscalation == nil {
		t.Fatal("AllowPrivilegeEscalation is nil, expected false")
	}
	if *c.SecurityContext.AllowPrivilegeEscalation {
		t.Error("AllowPrivilegeEscalation must be false (Requirement 3.1)")
	}
}

// ---------------------------------------------------------------------------
// Requirement 3.5 — tmpfs volume at /tmp
// ---------------------------------------------------------------------------

// TestBuildPod_TmpfsVolumeAtTmp verifies that the tmpfs volume is mounted at
// /tmp with the correct size derived from TmpfsSizeGiB (Requirement 3.5).
func TestBuildPod_TmpfsVolumeAtTmp(t *testing.T) {
	spec := defaultSpec()
	spec.TmpfsSizeGiB = 1
	pod := podbuilder.BuildPod("default", spec)
	c := submissionContainer(t, pod)

	// Find the /tmp volume mount.
	var found bool
	for _, vm := range c.VolumeMounts {
		if vm.MountPath == "/tmp" {
			found = true
			break
		}
	}
	if !found {
		t.Fatal("no volume mount at /tmp found")
	}

	// Find the corresponding volume definition.
	var vol *corev1.Volume
	for i := range pod.Spec.Volumes {
		if pod.Spec.Volumes[i].Name == "tmp-vol" {
			vol = &pod.Spec.Volumes[i]
			break
		}
	}
	if vol == nil {
		t.Fatal("volume 'tmp-vol' not found in pod spec")
	}
	if vol.EmptyDir == nil {
		t.Fatal("volume 'tmp-vol' has no EmptyDir source")
	}
	if vol.EmptyDir.Medium != corev1.StorageMediumMemory {
		t.Errorf("tmpfs medium: expected Memory, got %q", vol.EmptyDir.Medium)
	}
	if vol.EmptyDir.SizeLimit == nil {
		t.Fatal("tmpfs SizeLimit is nil")
	}

	expectedSize := "1Gi"
	if vol.EmptyDir.SizeLimit.String() != expectedSize {
		t.Errorf("tmpfs SizeLimit: expected %s, got %s", expectedSize, vol.EmptyDir.SizeLimit.String())
	}
}

// TestBuildPod_TmpfsSizeRespectsSpec verifies that different TmpfsSizeGiB
// values in the spec produce correspondingly different volume size limits.
func TestBuildPod_TmpfsSizeRespectsSpec(t *testing.T) {
	cases := []struct {
		tmpfsGiB int32
		expected string
	}{
		{1, "1Gi"},
		{2, "2Gi"},
	}

	for _, tc := range cases {
		t.Run(tc.expected, func(t *testing.T) {
			spec := defaultSpec()
			spec.TmpfsSizeGiB = tc.tmpfsGiB
			pod := podbuilder.BuildPod("default", spec)

			var vol *corev1.Volume
			for i := range pod.Spec.Volumes {
				if pod.Spec.Volumes[i].Name == "tmp-vol" {
					vol = &pod.Spec.Volumes[i]
					break
				}
			}
			if vol == nil || vol.EmptyDir == nil || vol.EmptyDir.SizeLimit == nil {
				t.Fatal("tmpfs volume not found or has no size limit")
			}
			if vol.EmptyDir.SizeLimit.String() != tc.expected {
				t.Errorf("TmpfsSizeGiB=%d: expected %s, got %s",
					tc.tmpfsGiB, tc.expected, vol.EmptyDir.SizeLimit.String())
			}
		})
	}
}

// ---------------------------------------------------------------------------
// Requirement 3.3 — memory limit
// ---------------------------------------------------------------------------

// TestBuildPod_MemoryLimitMatchesSpec verifies that the container memory
// limit matches the spec's MemoryLimitGiB value (Requirement 3.3).
func TestBuildPod_MemoryLimitMatchesSpec(t *testing.T) {
	spec := defaultSpec()
	spec.MemoryLimitGiB = 4
	pod := podbuilder.BuildPod("default", spec)
	c := submissionContainer(t, pod)

	memLimit := c.Resources.Limits[corev1.ResourceMemory]
	memRequest := c.Resources.Requests[corev1.ResourceMemory]

	expectedMem := "4Gi"
	if memLimit.String() != expectedMem {
		t.Errorf("memory limit: expected %s, got %s", expectedMem, memLimit.String())
	}
	if memRequest.String() != expectedMem {
		t.Errorf("memory request: expected %s, got %s (must equal limit for Guaranteed QoS)", expectedMem, memRequest.String())
	}
}

// ---------------------------------------------------------------------------
// Requirement 3.2 — CPU pinning via Guaranteed QoS
// ---------------------------------------------------------------------------

// TestBuildPod_CPURequestEqualsLimit verifies that CPU requests == limits,
// which is required for Guaranteed QoS and therefore kubelet CPU Manager
// static policy core pinning (Requirement 3.2).
func TestBuildPod_CPURequestEqualsLimit(t *testing.T) {
	spec := defaultSpec()
	spec.CPUCores = 4
	pod := podbuilder.BuildPod("default", spec)
	c := submissionContainer(t, pod)

	cpuReq := c.Resources.Requests[corev1.ResourceCPU]
	cpuLim := c.Resources.Limits[corev1.ResourceCPU]

	if cpuReq.Cmp(cpuLim) != 0 {
		t.Errorf("CPU request (%s) must equal CPU limit (%s) for Guaranteed QoS (Requirement 3.2)",
			cpuReq.String(), cpuLim.String())
	}

	expected := "4"
	if cpuLim.String() != expected {
		t.Errorf("CPU limit: expected %s, got %s", expected, cpuLim.String())
	}
}

// ---------------------------------------------------------------------------
// Additional hardening checks
// ---------------------------------------------------------------------------

// TestBuildPod_AutomountServiceAccountTokenFalse verifies that the service
// account token is not mounted (defence-in-depth against API server access).
func TestBuildPod_AutomountServiceAccountTokenFalse(t *testing.T) {
	pod := podbuilder.BuildPod("default", defaultSpec())
	if pod.Spec.AutomountServiceAccountToken == nil {
		t.Fatal("AutomountServiceAccountToken is nil, expected false")
	}
	if *pod.Spec.AutomountServiceAccountToken {
		t.Error("AutomountServiceAccountToken must be false")
	}
}

// TestBuildPod_NodeSelectorRoleSandbox verifies that the pod is scheduled to
// sandbox-dedicated nodes.
func TestBuildPod_NodeSelectorRoleSandbox(t *testing.T) {
	pod := podbuilder.BuildPod("default", defaultSpec())
	if v := pod.Spec.NodeSelector["role"]; v != "sandbox" {
		t.Errorf("nodeSelector[role]: expected sandbox, got %q", v)
	}
}

// TestBuildPod_TolerationDedicatedSandbox verifies that the sandbox toleration
// is present so the pod can be scheduled onto tainted nodes.
func TestBuildPod_TolerationDedicatedSandbox(t *testing.T) {
	pod := podbuilder.BuildPod("default", defaultSpec())
	found := false
	for _, tol := range pod.Spec.Tolerations {
		if tol.Key == "dedicated" && tol.Value == "sandbox" && tol.Effect == corev1.TaintEffectNoSchedule {
			found = true
			break
		}
	}
	if !found {
		t.Error("toleration dedicated=sandbox:NoSchedule not found in pod spec")
	}
}

// TestBuildPod_SeccompProfileLocalhost verifies that the seccomp profile type
// is Localhost with the correct profile path (Requirement 3.1).
func TestBuildPod_SeccompProfileLocalhost(t *testing.T) {
	pod := podbuilder.BuildPod("default", defaultSpec())
	sc := pod.Spec.SecurityContext
	if sc == nil || sc.SeccompProfile == nil {
		t.Fatal("pod SecurityContext.SeccompProfile is nil")
	}
	if sc.SeccompProfile.Type != corev1.SeccompProfileTypeLocalhost {
		t.Errorf("SeccompProfile.Type: expected Localhost, got %q", sc.SeccompProfile.Type)
	}
	if sc.SeccompProfile.LocalhostProfile == nil || *sc.SeccompProfile.LocalhostProfile != "profiles/dbhp-sandbox.json" {
		t.Errorf("SeccompProfile.LocalhostProfile: expected profiles/dbhp-sandbox.json, got %v", sc.SeccompProfile.LocalhostProfile)
	}
}

// TestBuildPod_AppArmorAnnotationPresent verifies that the legacy AppArmor
// annotation is set on the pod (Requirement 3.1, pids.max=512 enforcement).
func TestBuildPod_AppArmorAnnotationPresent(t *testing.T) {
	pod := podbuilder.BuildPod("default", defaultSpec())
	const key = "container.apparmor.security.beta.kubernetes.io/submission"
	val, ok := pod.Annotations[key]
	if !ok {
		t.Fatalf("AppArmor annotation %q not found on pod", key)
	}
	if val != "localhost/dbhp-sandbox" {
		t.Errorf("AppArmor annotation: expected localhost/dbhp-sandbox, got %q", val)
	}
}

// TestBuildPod_ImageRefContainsSubmissionID verifies that the container image
// reference includes the submission ID so that the correct image is pulled.
func TestBuildPod_ImageRefContainsSubmissionID(t *testing.T) {
	spec := defaultSpec()
	spec.SubmissionID = "sub-test-999"
	pod := podbuilder.BuildPod("default", spec)
	c := submissionContainer(t, pod)

	if c.Image == "" {
		t.Fatal("container image is empty")
	}
	// The image must contain the submission ID regardless of registry prefix.
	const want = "sub-test-999"
	found := false
	for i := 0; i < len(c.Image)-len(want)+1; i++ {
		if c.Image[i:i+len(want)] == want {
			found = true
			break
		}
	}
	if !found {
		t.Errorf("container image %q does not contain submission ID %q", c.Image, want)
	}
}
