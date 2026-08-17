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
	"reflect"

	"github.com/go-logr/logr"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

// EnsureCertificate ensures a namespaced cert-manager Certificate exists and
// writes the requested TLS Secret using the provided issuer reference.
func EnsureCertificate(ctx context.Context, client client.Client, log *logr.Logger, namespace string, name string, secretName string, commonName string, dnsNames []string, issuerRef CertificateIssuerRef, owner metav1.OwnerReference) error {
	issuerRef = issuerRef.normalized()
	if err := validateCertificateIssuerRef(issuerRef); err != nil {
		return err
	}

	desiredSpec := map[string]interface{}{
		"secretName": secretName,
		"dnsNames":   stringSliceToInterfaces(dnsNames),
		"commonName": commonName,
		"privateKey": map[string]interface{}{
			"algorithm":      "RSA",
			"size":           int64(2048),
			"rotationPolicy": "Always",
		},
		"issuerRef": map[string]interface{}{
			"name":  issuerRef.Name,
			"kind":  IssuerKind,
			"group": CertManagerGroup,
		},
	}

	certificate := &unstructured.Unstructured{}
	certificate.SetGroupVersionKind(certificateGVK())
	key := types.NamespacedName{Name: name, Namespace: namespace}
	if err := client.Get(ctx, key, certificate); err == nil {
		currentSpec, _, _ := unstructured.NestedMap(certificate.Object, "spec")
		ownerChanged, err := SetOwnerReference(certificate, owner)
		if err != nil {
			return err
		}
		if reflect.DeepEqual(currentSpec, desiredSpec) && !ownerChanged {
			log.V(1).Info("Certificate had no drift", "namespace", namespace, "name", name, "secret", secretName)
			return nil
		}
		if !reflect.DeepEqual(currentSpec, desiredSpec) {
			if err := unstructured.SetNestedMap(certificate.Object, desiredSpec, "spec"); err != nil {
				return err
			}
		}
		if err := client.Update(ctx, certificate); err != nil {
			return err
		}
		log.V(1).Info("Certificate reconciled", "namespace", namespace, "certificate", name, "secret", secretName)
		return nil
	} else if !apierrors.IsNotFound(err) {
		return err
	}

	certificate = &unstructured.Unstructured{
		Object: map[string]interface{}{
			"apiVersion": "cert-manager.io/v1",
			"kind":       "Certificate",
			"metadata": map[string]interface{}{
				"name":      name,
				"namespace": namespace,
				"labels": map[string]interface{}{
					"app.kubernetes.io/managed-by": "neteye-operator",
				},
			},
			"spec": desiredSpec,
		},
	}
	if _, err := SetOwnerReference(certificate, owner); err != nil {
		return err
	}
	if err := client.Create(ctx, certificate); err != nil && !apierrors.IsAlreadyExists(err) {
		return err
	}
	log.Info("Certificate created", "namespace", namespace, "certificate", name, "secret", secretName)
	return nil
}

// IsCertificateReady reports whether cert-manager marked the Certificate Ready.
// It does not wait; callers should requeue and check again later.
func IsCertificateReady(ctx context.Context, client client.Client, namespace string, name string) (bool, string, error) {
	certificate := &unstructured.Unstructured{}
	certificate.SetGroupVersionKind(certificateGVK())
	key := types.NamespacedName{Name: name, Namespace: namespace}
	if err := client.Get(ctx, key, certificate); err != nil {
		if apierrors.IsNotFound(err) {
			return false, "waiting for TLS Certificate to be created", nil
		}
		return false, "", err
	}

	conditions, found, err := unstructured.NestedSlice(certificate.Object, "status", "conditions")
	if err != nil {
		return false, "", err
	}
	if !found || len(conditions) == 0 {
		return false, "waiting for TLS Certificate status conditions", nil
	}

	for _, rawCondition := range conditions {
		condition, ok := rawCondition.(map[string]interface{})
		if !ok {
			continue
		}
		conditionType, _, _ := unstructured.NestedString(condition, "type")
		if conditionType != "Ready" {
			continue
		}
		status, _, _ := unstructured.NestedString(condition, "status")
		if status == "True" {
			return true, "", nil
		}

		message, _, _ := unstructured.NestedString(condition, "message")
		if message == "" {
			reason, _, _ := unstructured.NestedString(condition, "reason")
			message = reason
		}
		if message == "" {
			message = "waiting for TLS Certificate Ready condition"
		}
		return false, message, nil
	}

	return false, "waiting for TLS Certificate Ready condition", nil
}

func certificateGVK() schema.GroupVersionKind {
	return schema.GroupVersionKind{
		Group:   "cert-manager.io",
		Version: "v1",
		Kind:    "Certificate",
	}
}

func stringSliceToInterfaces(values []string) []interface{} {
	items := make([]interface{}, 0, len(values))
	for _, value := range values {
		items = append(items, value)
	}
	return items
}
