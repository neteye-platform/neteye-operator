// SPDX-FileCopyrightText: 2026 Würth IT Italy S.r.l.
// SPDX-License-Identifier: Apache-2.0 OR MIT

package permissionsync

import (
	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/util/intstr"
	"k8s.io/utils/ptr"

	neteye "github.com/neteye-platform/neteye-operator/api/v1alpha1"
	"github.com/neteye-platform/neteye-operator/internal/permissionsyncconfig"
)

const (
	// DeploymentName is the PermissionSync workload.
	DeploymentName = "permissionsync"
	// ServiceName is its in-cluster ClusterIP Service. PermissionSync is not
	// exposed through the shared Gateway: its callers are in-cluster or
	// deployment-local, and TLS termination in front of the listener is a
	// deployment concern.
	ServiceName = "permissionsync"
	// IngressPolicyName and EgressPolicyName are the workload's Cilium
	// policies, composed with the namespace-wide default-deny baseline.
	IngressPolicyName = "neteye-permissionsync-ingress"
	EgressPolicyName  = "neteye-permissionsync-egress"

	appLabel      = permissionsyncconfig.WorkloadAppLabel
	containerName = "permissionsync"
	portName      = "http"
	// unprivilegedUser is the image's own dedicated account, declared
	// numerically because the image carries no passwd entry to resolve.
	unprivilegedUser = int64(65532)
	// postGraceMarginSeconds covers what PermissionSync still does after its
	// configured request grace expires: a fixed post-grace cancellation
	// window, then a bounded final lifecycle and trace cleanup. Both are
	// product values of a few seconds, so the pod's termination grace period
	// is the configured grace plus this margin — a horizon that is too short
	// simply means Kubernetes sends SIGKILL before the process finished its
	// own sequence.
	postGraceMarginSeconds = int64(10)
	// configFileMode keeps the mounted document readable by the container's
	// group only. Written in decimal because a leading-zero YAML integer is
	// ambiguous: 288 is 0440.
	configFileMode = int32(0o440)
	// configVersionAnnotation rolls the workload when the rendered document
	// changes. PermissionSync has no runtime reload, so a configuration change
	// has to reach the processes as a new revision.
	configVersionAnnotation = "neteye.cloud/config-resource-version"
)

func permissionSyncDeployment(namespace string, spec *neteye.NetEyePermissionSyncSpec, image string, annotations map[string]string) *appsv1.Deployment {
	labels := map[string]string{"app": appLabel}
	return &appsv1.Deployment{
		ObjectMeta: metav1.ObjectMeta{Name: DeploymentName, Namespace: namespace},
		Spec: appsv1.DeploymentSpec{
			Replicas: ptr.To(spec.EffectiveReplicas()),
			Selector: &metav1.LabelSelector{MatchLabels: labels},
			Template: corev1.PodTemplateSpec{
				ObjectMeta: metav1.ObjectMeta{Labels: labels, Annotations: annotations},
				Spec: corev1.PodSpec{
					TerminationGracePeriodSeconds: ptr.To(terminationGracePeriod(spec)),
					// PermissionSync calls no Kubernetes API, so it needs no
					// API credential.
					AutomountServiceAccountToken: ptr.To(false),
					SecurityContext: &corev1.PodSecurityContext{
						RunAsNonRoot: ptr.To(true),
						RunAsUser:    ptr.To(unprivilegedUser),
						RunAsGroup:   ptr.To(unprivilegedUser),
						// Lets the mounted document be group-readable by that
						// account without making it readable by anyone else.
						FSGroup:        ptr.To(unprivilegedUser),
						SeccompProfile: &corev1.SeccompProfile{Type: corev1.SeccompProfileTypeRuntimeDefault},
					},
					Containers: []corev1.Container{{
						Name:  containerName,
						Image: image,
						// The complete application configuration contract: one
						// external file path and no other environment value.
						Env:            []corev1.EnvVar{{Name: ConfigFileEnvVar, Value: ConfigMountPath + "/" + ConfigFileName}},
						Ports:          []corev1.ContainerPort{{Name: portName, ContainerPort: ListenerPort, Protocol: corev1.ProtocolTCP}},
						LivenessProbe:  httpProbe("/healthz", 10, 3),
						ReadinessProbe: httpProbe("/readyz", 5, 3),
						SecurityContext: &corev1.SecurityContext{
							Privileged:               ptr.To(false),
							AllowPrivilegeEscalation: ptr.To(false),
							RunAsNonRoot:             ptr.To(true),
							RunAsUser:                ptr.To(unprivilegedUser),
							RunAsGroup:               ptr.To(unprivilegedUser),
							// Nothing is written to the filesystem, so no
							// writable path is required anywhere.
							ReadOnlyRootFilesystem: ptr.To(true),
							Capabilities:           &corev1.Capabilities{Drop: []corev1.Capability{"ALL"}},
							SeccompProfile:         &corev1.SeccompProfile{Type: corev1.SeccompProfileTypeRuntimeDefault},
						},
						Resources: corev1.ResourceRequirements{
							Requests: corev1.ResourceList{corev1.ResourceCPU: resource.MustParse("100m"), corev1.ResourceMemory: resource.MustParse("64Mi")},
							Limits:   corev1.ResourceList{corev1.ResourceCPU: resource.MustParse("500m"), corev1.ResourceMemory: resource.MustParse("256Mi")},
						},
						VolumeMounts: []corev1.VolumeMount{{Name: "configuration", MountPath: ConfigMountPath, ReadOnly: true}},
					}},
					Volumes: []corev1.Volume{{Name: "configuration", VolumeSource: corev1.VolumeSource{Secret: &corev1.SecretVolumeSource{
						SecretName:  ConfigSecretName,
						DefaultMode: ptr.To(configFileMode),
						Items:       []corev1.KeyToPath{{Key: ConfigFileName, Path: ConfigFileName}},
					}}}},
				},
			},
		},
	}
}

// terminationGracePeriod derives the pod's grace period from the configured
// shutdown grace, rounding up to whole seconds, so a longer grace is actually
// honored instead of being cut short by a fixed horizon.
func terminationGracePeriod(spec *neteye.NetEyePermissionSyncSpec) int64 {
	grace := spec.Shutdown.EffectiveGraceMilliseconds()
	return (grace+999)/1000 + postGraceMarginSeconds
}

// httpProbe builds one operational-endpoint probe. Neither endpoint takes an
// inbound admission permit, so a saturated synchronization workload cannot
// keep the probes from being answered.
func httpProbe(path string, period, failures int32) *corev1.Probe {
	return &corev1.Probe{
		ProbeHandler:     corev1.ProbeHandler{HTTPGet: &corev1.HTTPGetAction{Path: path, Port: intstr.FromString(portName)}},
		PeriodSeconds:    period,
		FailureThreshold: failures,
		TimeoutSeconds:   2,
	}
}

func permissionSyncService(namespace string) *corev1.Service {
	return &corev1.Service{
		ObjectMeta: metav1.ObjectMeta{Name: ServiceName, Namespace: namespace},
		Spec: corev1.ServiceSpec{
			Type:     corev1.ServiceTypeClusterIP,
			Selector: map[string]string{"app": appLabel},
			Ports:    []corev1.ServicePort{{Name: portName, Protocol: corev1.ProtocolTCP, Port: ListenerPort, TargetPort: intstr.FromInt32(ListenerPort)}},
		},
	}
}
