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

package controller

import (
	"context"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	batchv1 "k8s.io/api/batch/v1"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"

	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"

	neteyecomv1alpha1 "github.com/neteye/neteye-platform-operator/api/v1alpha1"
)

var _ = Describe("Keycloak Controller", func() {
	Context("When reconciling a resource", func() {
		const resourceName = "test-resource"

		ctx := context.Background()

		typeNamespacedName := types.NamespacedName{
			Name:      resourceName,
			Namespace: "default", // TODO(user):Modify as needed
		}
		keycloak := &neteyecomv1alpha1.Keycloak{}

		reconciler := func() *KeycloakReconciler {
			return &KeycloakReconciler{
				Client:            k8sClient,
				Scheme:            k8sClient.Scheme(),
				APIReader:         k8sClient,
				OperatorNamespace: "neteye-system",
			}
		}

		// upstreamKeycloak is the k8s.keycloak.org CR the operator drives; the
		// upstream operator is not running here, so tests move its conditions
		// by hand to walk the stages.
		upstreamKeycloak := func() *unstructured.Unstructured {
			kc := &unstructured.Unstructured{}
			kc.SetGroupVersionKind(schema.GroupVersionKind{
				Group: "k8s.keycloak.org", Version: "v2beta1", Kind: "Keycloak",
			})
			return kc
		}

		// cert-manager is not running here, so tests write the Secret it would
		// have produced.
		issueCertificate := func() {
			secret := &corev1.Secret{
				ObjectMeta: metav1.ObjectMeta{Name: resourceName + "-tls", Namespace: "default"},
				Type:       corev1.SecretTypeTLS,
				Data: map[string][]byte{
					"tls.crt": []byte("-----BEGIN CERTIFICATE-----\nnot a real one\n-----END CERTIFICATE-----\n"),
					"tls.key": []byte("key"),
				},
			}
			Expect(client.IgnoreAlreadyExists(k8sClient.Create(ctx, secret))).To(Succeed())
		}

		markUpstreamReady := func() {
			kc := upstreamKeycloak()
			Expect(k8sClient.Get(ctx, typeNamespacedName, kc)).To(Succeed())
			Expect(unstructured.SetNestedSlice(kc.Object, []any{
				map[string]any{"type": "Ready", "status": "True"},
			}, "status", "conditions")).To(Succeed())
			Expect(k8sClient.Status().Update(ctx, kc)).To(Succeed())
		}

		// finishJob drives a Job to a terminal state the way the Job controller
		// does: the API server rejects a terminal condition without its
		// precursor and without a start time.
		finishJob := func(job *batchv1.Job, precursor, terminal batchv1.JobConditionType) {
			now := metav1.Now()
			job.Status.StartTime = &now
			if terminal == batchv1.JobComplete {
				job.Status.CompletionTime = &now
			}
			job.Status.Conditions = []batchv1.JobCondition{
				{Type: precursor, Status: corev1.ConditionTrue, LastProbeTime: now, LastTransitionTime: now},
				{Type: terminal, Status: corev1.ConditionTrue, LastProbeTime: now, LastTransitionTime: now},
			}
			Expect(k8sClient.Status().Update(ctx, job)).To(Succeed())
		}

		BeforeEach(func() {
			By("creating the custom resource for the Kind Keycloak")
			err := k8sClient.Get(ctx, typeNamespacedName, keycloak)
			if err != nil && errors.IsNotFound(err) {
				resource := &neteyecomv1alpha1.Keycloak{
					ObjectMeta: metav1.ObjectMeta{
						Name:      resourceName,
						Namespace: "default",
					},
					Spec: neteyecomv1alpha1.KeycloakSpec{
						Image:       "registry.example/neteye-keycloak:test",
						ConfigImage: "registry.example/neteye-keycloak-config:test",
						AdditionalOptions: []neteyecomv1alpha1.ServiceOption{
							{Name: "loginTheme", Value: "wp"},
							{Name: "loginThemee", Value: "typo"},
						},
					},
				}
				Expect(k8sClient.Create(ctx, resource)).To(Succeed())
			}
		})

		AfterEach(func() {
			resource := &neteyecomv1alpha1.Keycloak{}
			err := k8sClient.Get(ctx, typeNamespacedName, resource)
			Expect(err).NotTo(HaveOccurred())

			By("Cleanup the specific resource instance Keycloak")
			Expect(k8sClient.Delete(ctx, resource)).To(Succeed())

			// envtest runs no garbage collector, so the objects the instance
			// owns outlive it here and would be found by the next spec.
			owned := []client.Object{
				&batchv1.Job{ObjectMeta: metav1.ObjectMeta{Name: resourceName + "-config", Namespace: "default"}},
				&corev1.Secret{ObjectMeta: metav1.ObjectMeta{Name: resourceName + "-db", Namespace: "default"}},
				&corev1.Secret{ObjectMeta: metav1.ObjectMeta{Name: resourceName + "-tls", Namespace: "default"}},
				&corev1.Secret{ObjectMeta: metav1.ObjectMeta{Name: resourceName + "-operator-client", Namespace: "default"}},
			}
			upstream := upstreamKeycloak()
			upstream.SetName(resourceName)
			upstream.SetNamespace("default")
			owned = append(owned, upstream)

			for _, gvk := range []schema.GroupVersionKind{
				{Group: "cert-manager.io", Version: "v1", Kind: "Certificate"},
				{Group: "cert-manager.io", Version: "v1", Kind: "Issuer"},
			} {
				o := &unstructured.Unstructured{}
				o.SetGroupVersionKind(gvk)
				o.SetNamespace("default")
				o.SetName(resourceName + map[string]string{"Certificate": "-tls", "Issuer": "-selfsigned"}[gvk.Kind])
				owned = append(owned, o)
			}

			for _, o := range owned {
				Expect(client.IgnoreNotFound(k8sClient.Delete(ctx, o))).To(Succeed())
			}
		})
		// An unrecognised option name is reported and ignored, never fatal: a
		// typo on an optional setting must not take the instance down, and an
		// older operator must still reconcile a CR written for a newer one.
		It("reports unrecognised options on a condition without failing", func() {
			By("Reconciling the created resource")
			_, err := reconciler().Reconcile(ctx, reconcile.Request{NamespacedName: typeNamespacedName})
			Expect(err).NotTo(HaveOccurred())

			kc := &neteyecomv1alpha1.Keycloak{}
			Expect(k8sClient.Get(ctx, typeNamespacedName, kc)).To(Succeed())

			cond := meta.FindStatusCondition(kc.Status.Conditions, neteyecomv1alpha1.ConditionAdditionalOptionsAccepted)
			Expect(cond).NotTo(BeNil())
			Expect(cond.Status).To(Equal(metav1.ConditionFalse))
			Expect(cond.Message).To(ContainSubstring("loginThemee"))
			Expect(kc.Status.Phase).NotTo(Equal(neteyecomv1alpha1.PhaseFailed))
		})

		// The upstream Keycloak Operator registers its CRD asynchronously.
		// Until it has, creating an upstream Keycloak CR is impossible, and
		// waiting for it is a normal state rather than a failure.
		It("waits for the Keycloak Operator instead of failing", func() {
			issueCertificate()
			_, err := reconciler().Reconcile(ctx, reconcile.Request{NamespacedName: typeNamespacedName})
			Expect(err).NotTo(HaveOccurred())

			By("installing the Keycloak Operator through OLM")
			ext := &unstructured.Unstructured{}
			ext.SetGroupVersionKind(schema.GroupVersionKind{
				Group: "olm.operatorframework.io", Version: "v1", Kind: "ClusterExtension",
			})
			Expect(k8sClient.Get(ctx, types.NamespacedName{Name: "keycloak-operator"}, ext)).To(Succeed())

			installNamespace, _, _ := unstructured.NestedString(ext.Object, "spec", "namespace")
			Expect(installNamespace).To(Equal("neteye-system"),
				"the cluster-wide install must be anchored to the operator's namespace, not a tenant's")
		})

		// cert-manager issues asynchronously, and Keycloak cannot start without
		// the Secret it mounts.
		It("waits for cert-manager before deploying Keycloak", func() {
			_, err := reconciler().Reconcile(ctx, reconcile.Request{NamespacedName: typeNamespacedName})
			Expect(err).NotTo(HaveOccurred())

			kc := &neteyecomv1alpha1.Keycloak{}
			Expect(k8sClient.Get(ctx, typeNamespacedName, kc)).To(Succeed())
			Expect(kc.Status.Phase).To(Equal(neteyecomv1alpha1.PhaseDeploying))

			cond := meta.FindStatusCondition(kc.Status.Conditions, neteyecomv1alpha1.ConditionAvailable)
			Expect(cond.Reason).To(Equal("WaitingForCertificate"))

			By("a Certificate having been requested from cert-manager")
			cert := &unstructured.Unstructured{}
			cert.SetGroupVersionKind(schema.GroupVersionKind{
				Group: "cert-manager.io", Version: "v1", Kind: "Certificate",
			})
			Expect(k8sClient.Get(ctx, types.NamespacedName{
				Name: resourceName + "-tls", Namespace: "default",
			}, cert)).To(Succeed())

			secretName, _, _ := unstructured.NestedString(cert.Object, "spec", "secretName")
			Expect(secretName).To(Equal(resourceName + "-tls"))
		})

		It("deploys, bootstraps and only then reports Ready", func() {
			By("the first pass: the instance is not up yet")
			issueCertificate()
			_, err := reconciler().Reconcile(ctx, reconcile.Request{NamespacedName: typeNamespacedName})
			Expect(err).NotTo(HaveOccurred())

			kc := &neteyecomv1alpha1.Keycloak{}
			Expect(k8sClient.Get(ctx, typeNamespacedName, kc)).To(Succeed())
			Expect(kc.Status.Phase).To(Equal(neteyecomv1alpha1.PhaseDeploying))

			By("the instance's own Secrets being created and owned by it")
			secret := &corev1.Secret{}
			Expect(k8sClient.Get(ctx, types.NamespacedName{
				Name: resourceName + "-db", Namespace: "default",
			}, secret)).To(Succeed())
			Expect(secret.OwnerReferences).NotTo(BeEmpty())

			By("Keycloak reporting Ready, which moves the instance to bootstrapping")
			markUpstreamReady()
			_, err = reconciler().Reconcile(ctx, reconcile.Request{NamespacedName: typeNamespacedName})
			Expect(err).NotTo(HaveOccurred())

			Expect(k8sClient.Get(ctx, typeNamespacedName, kc)).To(Succeed())
			Expect(kc.Status.Phase).To(Equal(neteyecomv1alpha1.PhaseBootstrapping))

			job := &batchv1.Job{}
			Expect(k8sClient.Get(ctx, types.NamespacedName{
				Name: resourceName + "-config", Namespace: "default",
			}, job)).To(Succeed())
			Expect(job.Spec.Template.Spec.Containers[0].Image).To(Equal("registry.example/neteye-keycloak-config:test"))

			By("the bootstrap Job completing")
			finishJob(job, batchv1.JobSuccessCriteriaMet, batchv1.JobComplete)

			_, err = reconciler().Reconcile(ctx, reconcile.Request{NamespacedName: typeNamespacedName})
			Expect(err).NotTo(HaveOccurred())

			Expect(k8sClient.Get(ctx, typeNamespacedName, kc)).To(Succeed())
			Expect(kc.Status.Phase).To(Equal(neteyecomv1alpha1.PhaseReady))
			Expect(kc.Status.Endpoint).To(ContainSubstring("default.svc"))
			Expect(meta.IsStatusConditionTrue(kc.Status.Conditions, neteyecomv1alpha1.ConditionAvailable)).To(BeTrue())

			// There is no Keycloak listening in envtest, so enforcement cannot
			// authenticate. That has to read as a degraded instance, not a
			// failed one — the deployment itself is fine.
			By("enforcement failing without taking the instance out of Ready")
			cond := meta.FindStatusCondition(kc.Status.Conditions, neteyecomv1alpha1.ConditionSettingsEnforced)
			Expect(cond).NotTo(BeNil(), "enforcement must run as soon as the instance is Ready")
			Expect(cond.Status).To(Equal(metav1.ConditionFalse))
			Expect(kc.Status.Phase).To(Equal(neteyecomv1alpha1.PhaseReady))
		})

		// The Job is garbage-collected an hour after it finishes. Keying
		// bootstrap to the Job object rather than to its inputs would drop the
		// instance back to Bootstrapping every time that happened — and
		// suspend enforcement for the whole of every re-bootstrap.
		It("stays Ready after the bootstrap Job is garbage-collected", func() {
			issueCertificate()
			_, err := reconciler().Reconcile(ctx, reconcile.Request{NamespacedName: typeNamespacedName})
			Expect(err).NotTo(HaveOccurred())
			markUpstreamReady()
			_, err = reconciler().Reconcile(ctx, reconcile.Request{NamespacedName: typeNamespacedName})
			Expect(err).NotTo(HaveOccurred())

			job := &batchv1.Job{}
			jobKey := types.NamespacedName{Name: resourceName + "-config", Namespace: "default"}
			Expect(k8sClient.Get(ctx, jobKey, job)).To(Succeed())
			finishJob(job, batchv1.JobSuccessCriteriaMet, batchv1.JobComplete)

			_, err = reconciler().Reconcile(ctx, reconcile.Request{NamespacedName: typeNamespacedName})
			Expect(err).NotTo(HaveOccurred())

			kc := &neteyecomv1alpha1.Keycloak{}
			Expect(k8sClient.Get(ctx, typeNamespacedName, kc)).To(Succeed())
			Expect(kc.Status.Phase).To(Equal(neteyecomv1alpha1.PhaseReady))
			Expect(kc.Status.BootstrapConfigHash).NotTo(BeEmpty(), "the successful run has to be remembered")

			By("the Job's TTL expiring")
			finishedUID := job.UID
			Expect(k8sClient.Delete(ctx, job, client.PropagationPolicy(metav1.DeletePropagationBackground))).To(Succeed())

			_, err = reconciler().Reconcile(ctx, reconcile.Request{NamespacedName: typeNamespacedName})
			Expect(err).NotTo(HaveOccurred())

			Expect(k8sClient.Get(ctx, typeNamespacedName, kc)).To(Succeed())
			Expect(kc.Status.Phase).To(Equal(neteyecomv1alpha1.PhaseReady),
				"an absent Job must not read as an un-bootstrapped instance")
			// envtest runs no Job controller, so the deleted object may linger;
			// what matters is that no new one was created in its place.
			if err := k8sClient.Get(ctx, jobKey, job); err == nil {
				Expect(job.UID).To(Equal(finishedUID),
					"the Job was recreated although its inputs have not changed")
			}
		})

		It("reports a failed bootstrap Job as a failed instance", func() {
			markReadyThroughBootstrap := func() *batchv1.Job {
				issueCertificate()
				_, err := reconciler().Reconcile(ctx, reconcile.Request{NamespacedName: typeNamespacedName})
				Expect(err).NotTo(HaveOccurred())
				markUpstreamReady()
				_, err = reconciler().Reconcile(ctx, reconcile.Request{NamespacedName: typeNamespacedName})
				Expect(err).NotTo(HaveOccurred())

				job := &batchv1.Job{}
				Expect(k8sClient.Get(ctx, types.NamespacedName{
					Name: resourceName + "-config", Namespace: "default",
				}, job)).To(Succeed())
				return job
			}

			finishJob(markReadyThroughBootstrap(), batchv1.JobFailureTarget, batchv1.JobFailed)

			_, err := reconciler().Reconcile(ctx, reconcile.Request{NamespacedName: typeNamespacedName})
			Expect(err).NotTo(HaveOccurred(), "the Job exhausted its own retries; requeueing faster would not help")

			kc := &neteyecomv1alpha1.Keycloak{}
			Expect(k8sClient.Get(ctx, typeNamespacedName, kc)).To(Succeed())
			Expect(kc.Status.Phase).To(Equal(neteyecomv1alpha1.PhaseFailed))
			Expect(meta.IsStatusConditionTrue(kc.Status.Conditions, neteyecomv1alpha1.ConditionAvailable)).To(BeFalse())
		})
	})
})
