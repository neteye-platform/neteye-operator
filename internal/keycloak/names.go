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

import "fmt"

const (
	// httpRelativePath must match the --http-relative-path baked into the
	// NetEye Keycloak image at build time. The image is started with
	// startOptimized, so a runtime value that disagrees with the baked-in one
	// is a hard failure rather than a silent rebuild: it has to be passed
	// through on every apply, and every URL the operator builds has to carry
	// it.
	httpRelativePath = "/auth"

	// managedByLabel marks every object this operator creates, so a human
	// reading the namespace can tell what is theirs and what is ours.
	managedByLabel = "app.kubernetes.io/managed-by"
	managedByValue = "neteye-operator"

	// configHashAnnotation stores a hash of the bootstrap Job's meaningful
	// inputs, so the Job is recreated when — and only when — they change.
	configHashAnnotation = "neteye.com/config-hash"
)

// names holds the names of every object one Keycloak instance is made of.
//
// They are all derived from the instance name rather than being constants,
// because two NetEye instances may target the same namespace; with fixed
// names they would silently reconcile over each other's Secrets and Job.
type names struct {
	// UpstreamKeycloak is the k8s.keycloak.org/Keycloak CR driven underneath.
	UpstreamKeycloak string

	// InitialAdminSecret is the basic-auth Secret the upstream Keycloak
	// Operator generates; its name is derived by upstream from the CR name
	// and cannot be chosen.
	InitialAdminSecret string

	// Certificate is the cert-manager Certificate producing TLSSecret.
	Certificate string

	// SelfSignedIssuer is the issuer the operator provisions when the spec
	// names none.
	SelfSignedIssuer string

	DBSecret     string
	TLSSecret    string
	ClientSecret string
	BootstrapJob string
}

func namesFor(instance string) names {
	return names{
		UpstreamKeycloak:   instance,
		InitialAdminSecret: instance + "-initial-admin",
		Certificate:        instance + "-tls",
		SelfSignedIssuer:   instance + "-selfsigned",
		DBSecret:           instance + "-db",
		TLSSecret:          instance + "-tls",
		ClientSecret:       instance + "-operator-client",
		BootstrapJob:       instance + "-config",
	}
}

// ServiceURL is the in-cluster address of the Keycloak instance, including the
// relative path baked into the NetEye image. The upstream operator names the
// Service after the CR with a -service suffix.
func ServiceURL(instance, namespace string) string {
	return fmt.Sprintf("https://%s-service.%s.svc:8443%s", instance, namespace, httpRelativePath)
}

// defaultHostname is the hostname the instance's certificate is issued for
// when the spec does not name one. It matches the in-cluster Service so the
// certificate is valid for the address the operator itself connects to.
func defaultHostname(instance, namespace string) string {
	return fmt.Sprintf("%s-service.%s.svc", instance, namespace)
}
