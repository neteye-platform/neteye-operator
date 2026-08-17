/*
Copyright 2026 Wuerth IT | Italy.

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

package resources

import (
	"context"
	"fmt"

	"github.com/go-logr/logr"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

const (
	CertManagerGroup = "cert-manager.io"
	IssuerKind       = "Issuer"
)

// CertificateIssuerRef identifies the namespaced cert-manager Issuer used by
// Certificates created for NetEye components.
type CertificateIssuerRef struct {
	Name string
}

func (ref CertificateIssuerRef) normalized() CertificateIssuerRef {
	return ref
}

// EnsureIssuerExists ensures the referenced user-managed cert-manager Issuer
// exists in the NetEye namespace. The operator never creates or adopts Issuers
// because Issuer configuration represents a CA/trust decision owned by the user.
func EnsureIssuerExists(ctx context.Context, client client.Client, log logr.Logger, namespace string, ref CertificateIssuerRef) error {
	ref = ref.normalized()
	if err := validateCertificateIssuerRef(ref); err != nil {
		return err
	}

	issuer := &unstructured.Unstructured{}
	issuer.SetGroupVersionKind(certificateIssuerGVK(ref))
	key := issuerObjectKey(namespace, ref)
	if err := client.Get(ctx, key, issuer); err == nil {
		log.Info("cert-manager Issuer found", "namespace", issuer.GetNamespace(), "issuer", ref.Name)
		return nil
	} else {
		return err
	}
}

func certificateIssuerGVK(ref CertificateIssuerRef) schema.GroupVersionKind {
	return schema.GroupVersionKind{
		Group:   CertManagerGroup,
		Version: "v1",
		Kind:    IssuerKind,
	}
}

func issuerObjectKey(namespace string, ref CertificateIssuerRef) types.NamespacedName {
	return types.NamespacedName{Name: ref.Name, Namespace: namespace}
}

func validateCertificateIssuerRef(ref CertificateIssuerRef) error {
	if ref.Name == "" {
		return fmt.Errorf("certificate issuer ref name is required")
	}
	return nil
}
