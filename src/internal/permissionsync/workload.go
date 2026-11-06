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
)

const (
	// DeploymentName is the PermissionSync workload of a tenant.
	DeploymentName = "permissionsync"
	// ServiceName is its ClusterIP Service. Nothing reaches it yet: its
	// network policy allows no ingress until Keycloak calls PermissionSync.
	ServiceName = "permissionsync"
	// AppLabel selects the PermissionSync Pods.
	AppLabel = "permissionsync"

	containerName = "permissionsync"
	portName      = "http"
	// unprivilegedUser is the account of the image, given as a number because
	// the image has no passwd entry.
	unprivilegedUser = int64(65532)
	// postGraceMarginSeconds covers the work after the shutdown grace: a
	// fixed cancellation window and the final cleanup.
	postGraceMarginSeconds = int64(10)
	// configFileMode makes the mounted file readable by the group of the
	// container only. 288 is 0440.
	configFileMode = int32(0o440)
	// ConfigurationHashAnnotation is a hash of the configuration file and of
	// the credentials. PermissionSync reads both only at startup, so a change
	// rolls out new Pods.
	ConfigurationHashAnnotation = "neteye.cloud/configuration-hash"
)

func podLabels() map[string]string {
	return map[string]string{"app": AppLabel}
}

func deployment(namespace string, spec *neteye.NetEyeTenantPermissionSyncSpec, image, configurationHash string) *appsv1.Deployment {
	return &appsv1.Deployment{
		ObjectMeta: metav1.ObjectMeta{Name: DeploymentName, Namespace: namespace},
		Spec: appsv1.DeploymentSpec{
			Replicas: ptr.To(spec.EffectiveReplicas()),
			Selector: &metav1.LabelSelector{MatchLabels: podLabels()},
			Template: corev1.PodTemplateSpec{
				ObjectMeta: metav1.ObjectMeta{Labels: podLabels(), Annotations: map[string]string{ConfigurationHashAnnotation: configurationHash}},
				Spec: corev1.PodSpec{
					TerminationGracePeriodSeconds: ptr.To((shutdownGraceMilliseconds+999)/1000 + postGraceMarginSeconds),
					// PermissionSync calls no Kubernetes API.
					AutomountServiceAccountToken: ptr.To(false),
					// Kubernetes adds variables for the Services of the namespace,
					// and the Service permissionsync gives them the prefix
					// PERMISSIONSYNC_, which PermissionSync reads as configuration.
					// PermissionSync finds nothing through them.
					EnableServiceLinks: ptr.To(false),
					SecurityContext: &corev1.PodSecurityContext{
						RunAsNonRoot:   ptr.To(true),
						RunAsUser:      ptr.To(unprivilegedUser),
						RunAsGroup:     ptr.To(unprivilegedUser),
						FSGroup:        ptr.To(unprivilegedUser),
						SeccompProfile: &corev1.SeccompProfile{Type: corev1.SeccompProfileTypeRuntimeDefault},
					},
					Containers: []corev1.Container{{
						Name:           containerName,
						Image:          image,
						Env:            environment(spec),
						Ports:          []corev1.ContainerPort{{Name: portName, ContainerPort: ListenerPort, Protocol: corev1.ProtocolTCP}},
						LivenessProbe:  httpProbe("/healthz", 10, 3),
						ReadinessProbe: httpProbe("/readyz", 5, 3),
						SecurityContext: &corev1.SecurityContext{
							Privileged:               ptr.To(false),
							AllowPrivilegeEscalation: ptr.To(false),
							RunAsNonRoot:             ptr.To(true),
							RunAsUser:                ptr.To(unprivilegedUser),
							RunAsGroup:               ptr.To(unprivilegedUser),
							ReadOnlyRootFilesystem:   ptr.To(true),
							Capabilities:             &corev1.Capabilities{Drop: []corev1.Capability{"ALL"}},
							SeccompProfile:           &corev1.SeccompProfile{Type: corev1.SeccompProfileTypeRuntimeDefault},
						},
						Resources: corev1.ResourceRequirements{
							Requests: corev1.ResourceList{corev1.ResourceCPU: resource.MustParse("50m"), corev1.ResourceMemory: resource.MustParse("64Mi")},
							Limits:   corev1.ResourceList{corev1.ResourceCPU: resource.MustParse("500m"), corev1.ResourceMemory: resource.MustParse("256Mi")},
						},
						VolumeMounts: []corev1.VolumeMount{{Name: "configuration", MountPath: ConfigMountPath, ReadOnly: true}},
					}},
					Volumes: []corev1.Volume{{Name: "configuration", VolumeSource: corev1.VolumeSource{ConfigMap: &corev1.ConfigMapVolumeSource{
						LocalObjectReference: corev1.LocalObjectReference{Name: ConfigMapName},
						DefaultMode:          ptr.To(configFileMode),
						Items:                []corev1.KeyToPath{{Key: ConfigFileName, Path: ConfigFileName}},
					}}}},
				},
			},
		},
	}
}

// environment returns the variables that the operator manages, then the
// variables of podExtraEnvVars. The GLPI tokens come from the Secret of the
// tenant and never appear in a resource.
func environment(spec *neteye.NetEyeTenantPermissionSyncSpec) []corev1.EnvVar {
	env := []corev1.EnvVar{{Name: configFileEnvVar, Value: ConfigMountPath + "/" + ConfigFileName}}
	if spec.GLPI != nil {
		credentials := spec.GLPI.CredentialsSecretRef
		env = append(env,
			secretEnvVar(glpiAppTokenEnvVar, credentials.Name, credentials.EffectiveAppTokenKey()),
			secretEnvVar(glpiUserTokenEnvVar, credentials.Name, credentials.EffectiveUserTokenKey()),
		)
	}
	for _, variable := range spec.PodExtraEnvVars {
		if neteye.PermissionSyncEnvVarApplies(spec, variable.Name) {
			env = append(env, corev1.EnvVar{Name: variable.Name, Value: variable.Value})
		}
	}
	return env
}

func secretEnvVar(name, secretName, key string) corev1.EnvVar {
	return corev1.EnvVar{Name: name, ValueFrom: &corev1.EnvVarSource{SecretKeyRef: &corev1.SecretKeySelector{
		LocalObjectReference: corev1.LocalObjectReference{Name: secretName},
		Key:                  key,
	}}}
}

// httpProbe builds a probe of an operational endpoint. These endpoints take no
// admission permit, so a saturated workload still answers them.
func httpProbe(path string, period, failures int32) *corev1.Probe {
	return &corev1.Probe{
		ProbeHandler:     corev1.ProbeHandler{HTTPGet: &corev1.HTTPGetAction{Path: path, Port: intstr.FromString(portName)}},
		PeriodSeconds:    period,
		FailureThreshold: failures,
		// Longer than the metadata timeout of 3 s, so that a probe does not
		// fail while a key refresh is still within its budget.
		TimeoutSeconds: 4,
	}
}

func service(namespace string) *corev1.Service {
	return &corev1.Service{
		ObjectMeta: metav1.ObjectMeta{Name: ServiceName, Namespace: namespace},
		Spec: corev1.ServiceSpec{
			Type:     corev1.ServiceTypeClusterIP,
			Selector: podLabels(),
			Ports:    []corev1.ServicePort{{Name: portName, Protocol: corev1.ProtocolTCP, Port: ListenerPort, TargetPort: intstr.FromString(portName)}},
		},
	}
}
