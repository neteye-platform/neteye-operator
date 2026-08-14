/*
Copyright (c) 2026 Würth IT Italy S.r.l.

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0

Unless required by applicable law or agreed to in writing, software
distributed under the License is distributed on an "AS IS" BASIS,
WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
See the License for the specific language governing permissions and
limitations under the License.
*/

package keycloak

import (
	"crypto/sha256"
	"encoding/hex"

	batchv1 "k8s.io/api/batch/v1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime/schema"

	neteyev1alpha1 "github.com/neteye/neteye-platform-operator/api/v1alpha1"
)

// instance is everything the resource builders need to describe one Keycloak
// deployment. It is the Keycloak CR reduced to the fields that shape objects,
// so the builders below stay pure and testable without a cluster.
type instance struct {
	Name        string
	Namespace   string
	Image       string
	ConfigImage string
	Hostname    string
	IssuerRef   *neteyev1alpha1.IssuerReference
}

// issuer is the cert-manager issuer signing this instance's certificate: the
// one the spec names, or the self-signed Issuer the operator provisions for
// instances nothing outside this operator talks to.
func (i instance) issuer() neteyev1alpha1.IssuerReference {
	if i.IssuerRef != nil {
		kind := i.IssuerRef.Kind
		if kind == "" {
			kind = "Issuer"
		}
		return neteyev1alpha1.IssuerReference{Name: i.IssuerRef.Name, Kind: kind}
	}
	return neteyev1alpha1.IssuerReference{Name: i.names().SelfSignedIssuer, Kind: "Issuer"}
}

// ownsIssuer reports whether the operator has to provision the issuer itself.
func (i instance) ownsIssuer() bool { return i.IssuerRef == nil }

// certificateSpec is the cert-manager Certificate that produces the Secret the
// Keycloak instance serves on. cert-manager owns renewal, which is why the
// operator issues nothing itself.
func certificateSpec(i instance) map[string]any {
	issuer := i.issuer()

	return map[string]any{
		"secretName": i.names().TLSSecret,
		"commonName": i.hostname(),
		// Both names matter: Keycloak is addressed by its configured hostname
		// from outside, and by its in-cluster Service by this operator.
		"dnsNames": []any{
			i.hostname(),
			defaultHostname(i.Name, i.Namespace),
		},
		"privateKey": map[string]any{
			"algorithm": "ECDSA",
			"size":      int64(256),
			// A rotated key on renewal means a compromised key stops being
			// useful once the certificate it belongs to expires.
			"rotationPolicy": "Always",
		},
		"usages": []any{"server auth"},
		"issuerRef": map[string]any{
			"name":  issuer.Name,
			"kind":  issuer.Kind,
			"group": "cert-manager.io",
		},
	}
}

// selfSignedIssuerSpec is the fallback issuer: it signs the instance's
// certificate with itself. That is enough because the operator pins the
// resulting certificate rather than chaining to a trusted root; an
// installation that needs the certificate trusted more widely names its own
// issuer on the spec instead.
func selfSignedIssuerSpec() map[string]any {
	return map[string]any{"selfSigned": map[string]any{}}
}

func (i instance) names() names { return namesFor(i.Name) }

func (i instance) hostname() string {
	if i.Hostname != "" {
		return i.Hostname
	}
	return defaultHostname(i.Name, i.Namespace)
}

func (i instance) labels() map[string]string {
	return map[string]string{
		managedByLabel:               managedByValue,
		"app.kubernetes.io/part-of":  "neteye",
		"app.kubernetes.io/instance": i.Name,
	}
}

// upstreamKeycloakSpec is the spec of the k8s.keycloak.org/Keycloak CR this
// operator drives underneath. It is built as an untyped map because the
// upstream API's Go types are not a dependency of this project: adding them
// would pin us to one upstream release, and the operator only ever writes a
// fixed shape and reads two condition fields back.
func upstreamKeycloakSpec(i instance) map[string]any {
	n := i.names()

	return map[string]any{
		"instances": int64(1),
		"image":     i.Image,
		// The NetEye image already ran `kc.sh build` at image build time; the
		// operator must not re-augment it at startup.
		"startOptimized": true,
		"db": map[string]any{
			// TODO(db): points at an external PostgreSQL. The platform is
			// moving to a CNPG Cluster per service; this block is what that
			// change replaces.
			"vendor":   "postgres",
			"host":     dbHost(i.Namespace),
			"port":     int64(5432),
			"database": "keycloak",
			"usernameSecret": map[string]any{
				"name": n.DBSecret,
				"key":  "username",
			},
			"passwordSecret": map[string]any{
				"name": n.DBSecret,
				"key":  "password",
			},
		},
		"http": map[string]any{
			"tlsSecret": n.TLSSecret,
		},
		"hostname": map[string]any{
			"hostname": i.hostname(),
			"strict":   false,
		},
		"proxy": map[string]any{
			"headers": "xforwarded",
		},
		"additionalOptions": []any{
			map[string]any{
				"name":  "http-relative-path",
				"value": httpRelativePath,
			},
		},
	}
}

func dbHost(namespace string) string {
	return "postgres-db." + namespace + ".svc"
}

// configHash fingerprints the bootstrap Job's meaningful inputs. Job specs are
// immutable, so a changed input can only be applied by deleting and recreating
// the Job; this hash is what tells the two cases apart.
func configHash(i instance, clientSecret string) string {
	sum := sha256.Sum256([]byte(i.Name + "|" + i.Namespace + "|" + i.ConfigImage + "|" + clientSecret))
	return hex.EncodeToString(sum[:])[:16]
}

// bootstrapJob is the one-shot Job that configures a fresh instance: realm,
// clients, themes, auth flows. It runs once and cannot correct drift — that is
// the enforcer's job, and the reason the two are separate.
func bootstrapJob(i instance, hash string) *batchv1.Job {
	n := i.names()

	backoffLimit := int32(6)
	ttl := int32(3600)
	deadline := int64(1800)

	secretEnv := func(name, secret, key string) corev1.EnvVar {
		return corev1.EnvVar{
			Name: name,
			ValueFrom: &corev1.EnvVarSource{
				SecretKeyRef: &corev1.SecretKeySelector{
					LocalObjectReference: corev1.LocalObjectReference{Name: secret},
					Key:                  key,
				},
			},
		}
	}

	return &batchv1.Job{
		ObjectMeta: metav1.ObjectMeta{
			Name:        n.BootstrapJob,
			Namespace:   i.Namespace,
			Labels:      i.labels(),
			Annotations: map[string]string{configHashAnnotation: hash},
		},
		Spec: batchv1.JobSpec{
			BackoffLimit: &backoffLimit,
			// Without a TTL finished Jobs pile up; without a deadline a hung
			// bootstrap never fails, so the phase never moves off Bootstrapping.
			TTLSecondsAfterFinished: &ttl,
			ActiveDeadlineSeconds:   &deadline,
			Template: corev1.PodTemplateSpec{
				ObjectMeta: metav1.ObjectMeta{Labels: i.labels()},
				Spec: corev1.PodSpec{
					RestartPolicy: corev1.RestartPolicyOnFailure,
					Containers: []corev1.Container{{
						Name:  "keycloak-config",
						Image: i.ConfigImage,
						Env: []corev1.EnvVar{
							{Name: "KEYCLOAK_URL", Value: ServiceURL(i.Name, i.Namespace)},
							// Credentials come from Secrets, never inlined: a Job
							// spec is readable by anyone who can get Jobs.
							secretEnv("KEYCLOAK_ADMIN_USER", n.InitialAdminSecret, "username"),
							secretEnv("KEYCLOAK_ADMIN_PASSWORD", n.InitialAdminSecret, "password"),
							// The role registers the operator's service-account
							// client with this value, so enforcement can then
							// authenticate as itself.
							secretEnv("KEYCLOAK_OPERATOR_CLIENT_SECRET", n.ClientSecret, clientSecretKey),
						},
					}},
				},
			},
		},
	}
}

func certificateGVK() schema.GroupVersionKind {
	return schema.GroupVersionKind{Group: "cert-manager.io", Version: "v1", Kind: "Certificate"}
}

func issuerGVK() schema.GroupVersionKind {
	return schema.GroupVersionKind{Group: "cert-manager.io", Version: "v1", Kind: "Issuer"}
}

func upstreamKeycloakGVK() schema.GroupVersionKind {
	return schema.GroupVersionKind{Group: "k8s.keycloak.org", Version: "v2beta1", Kind: "Keycloak"}
}

func crdGVK() schema.GroupVersionKind {
	return schema.GroupVersionKind{Group: "apiextensions.k8s.io", Version: "v1", Kind: "CustomResourceDefinition"}
}

func clusterExtensionGVK() schema.GroupVersionKind {
	return schema.GroupVersionKind{Group: "olm.operatorframework.io", Version: "v1", Kind: "ClusterExtension"}
}
