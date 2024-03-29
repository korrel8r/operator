// Copyright: This file is part of korrel8r, released under https://github.com/korrel8r/korrel8r/blob/main/LICENSE
package controllers

import (
	"context"
	"fmt"
	"os"
	"reflect"
	"time"

	"github.com/go-logr/logr"
	"github.com/go-logr/stdr"
	korrel8rv1alpha1 "github.com/korrel8r/operator/api/v1alpha1"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	routev1 "github.com/openshift/api/route/v1"
	"gopkg.in/yaml.v2"
	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	rbacv1 "k8s.io/api/rbac/v1"
	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/tools/record"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"
)

const Korrel8rName = "test-korrel8r"

var _ = Describe("Korrel8r controller", func() {
	Context("Korrel8r controller test", func() {
		ctx := logr.NewContext(context.Background(), stdr.New(nil))

		namespace := &corev1.Namespace{
			ObjectMeta: metav1.ObjectMeta{
				Name:      Korrel8rName,
				Namespace: Korrel8rName,
			},
		}

		nsName := types.NamespacedName{Name: Korrel8rName, Namespace: Korrel8rName}

		BeforeEach(func() {
			By("Creating the Namespace to perform the tests: " + namespace.Name)
			err := k8sClient.Create(ctx, namespace)
			Expect(err).To(Not(HaveOccurred()))
			err = os.Setenv(ImageEnv, "github.com/korrel8r/korrel8r:latest")
			Expect(err).To(Not(HaveOccurred()))
		})

		AfterEach(func() {
			// TODO(user): Attention if you improve this code by adding other context test you MUST
			// be aware of the current delete namespace limitations.
			// More info: https://book.kubebuilder.io/reference/envtest.html#testing-considerations
			By("Deleting the Namespace to perform the tests")
			_ = k8sClient.Delete(ctx, namespace)
			_ = os.Unsetenv(ImageEnv)
		})

		It("should successfully reconcile a custom resource for Korrel8r", func() {
			By("Creating the custom resource for the Kind Korrel8r")
			korrel8r := &korrel8rv1alpha1.Korrel8r{}
			korrel8rYAML := `
apiVersion: korrel8r.openshift.io/v1alpha1
kind: Korrel8r
spec:
  config:
    rules:
      - name: testrule
        start:
          domain: "x"
          classes: [foo]
        goal:
          domain: "y"
          classes: [bar]
        result:
          query: "y:bar:where can I find a good bar?"
`
			Expect(yaml.Unmarshal([]byte(korrel8rYAML), korrel8r)).To(Succeed())
			korrel8r.SetName(nsName.Name)
			korrel8r.SetNamespace(nsName.Namespace)
			Expect(k8sClient.Create(ctx, korrel8r)).To(Succeed())

			gvk := schema.FromAPIVersionAndKind(korrel8rv1alpha1.GroupVersion.String(), reflect.TypeOf(korrel8r).Elem().Name())
			ownerRef := *metav1.NewControllerRef(korrel8r, gvk)    // Expected owner ref
			eventuallyArgs := []any{time.Second, time.Second / 10} // Timeout for all Eventually() tests

			By("Reconciling the custom resource created")
			korrel8rReconciler := NewKorrel8rReconciler(k8sClient, k8sClient.Scheme(), record.NewFakeRecorder(1000))
			result, err := korrel8rReconciler.Reconcile(ctx, reconcile.Request{NamespacedName: nsName})
			Expect(err).NotTo(HaveOccurred())
			Expect(result).To(BeZero())

			{
				By("Checking if ConfigMap was successfully created in the reconciliation")
				found := &corev1.ConfigMap{}
				Eventually(func() error {
					return k8sClient.Get(ctx, nsName, found)
				}, eventuallyArgs...).Should(Succeed())
				Expect(found.GetOwnerReferences()).To(ContainElement(ownerRef))
				config, err := yaml.Marshal(korrel8r.Spec.Config)
				Expect(err).NotTo(HaveOccurred())
				Expect(found.Data).To(Equal(map[string]string{ConfigKey: string(config)}))
			}

			var labels map[string]string
			{
				By("Checking if Deployment was successfully created in the reconciliation")
				found := &appsv1.Deployment{}
				Eventually(func() error { return k8sClient.Get(ctx, nsName, found) }, eventuallyArgs...).Should(Succeed())
				Expect(found.GetOwnerReferences()).To(ContainElement(ownerRef))
				labels = found.Spec.Selector.MatchLabels
			}

			{
				By("Checking if Service was successfully created in the reconciliation")
				found := &corev1.Service{}
				Eventually(func() error { return k8sClient.Get(ctx, nsName, found) }, eventuallyArgs...).Should(Succeed())
				Expect(found.GetOwnerReferences()).To(ContainElement(ownerRef))
				Expect(found.Spec.Selector).To(Equal(labels), "service and deployment labels don't match")
			}

			{
				By("Checking if Service was successfully created in the reconciliation")
				found := &corev1.Service{}
				Eventually(func() error { return k8sClient.Get(ctx, nsName, found) }, eventuallyArgs...).Should(Succeed())
				Expect(found.GetOwnerReferences()).To(ContainElement(ownerRef))
				Expect(found.Spec.Selector).To(Equal(labels), "service and deployment labels don't match")
			}

			{
				By("Checking if ServiceAccount was successfully created in the reconciliation")
				found := &corev1.ServiceAccount{}
				Eventually(func() error { return k8sClient.Get(ctx, nsName, found) }, eventuallyArgs...).Should(Succeed())
				Expect(found.GetOwnerReferences()).To(ContainElement(ownerRef))
			}

			roleNN := types.NamespacedName{Name: RoleName}
			{
				By("Checking if ClusterRoleBinding was successfully created in the reconciliation")
				found := &rbacv1.ClusterRoleBinding{}
				Eventually(func() error { return k8sClient.Get(ctx, roleNN, found) }, eventuallyArgs...).Should(Succeed())
				subject := rbacv1.Subject{
					Kind:      "ServiceAccount",
					Name:      Korrel8rName,
					Namespace: Korrel8rName,
				}
				Expect(found.Subjects).To(Equal([]rbacv1.Subject{subject}))
			}
			{
				By("Checking Status Condition added to the Korrel8r instance")
				found := &korrel8rv1alpha1.Korrel8r{}
				Eventually(func() (err error) {
					if err = k8sClient.Get(ctx, nsName, found); err == nil {
						conditions := found.Status.Conditions
						if len(conditions) != 1 {
							return fmt.Errorf("expected 1 condition got %v, %v", len(conditions), conditions)
						}
						want := metav1.Condition{
							Type:    ConditionTypeAvailable,
							Status:  metav1.ConditionTrue,
							Reason:  "Reconciled",
							Message: "Ready",
						}
						got := conditions[0]
						// Only compare relevant fields, timestamp and generation fields will not match.
						if want.Type != got.Type || want.Status != got.Status || want.Reason != got.Reason || want.Message != got.Message {
							err = fmt.Errorf("expected %+v\nactual   %+v", want, got)
						}
					}
					return err
				}, eventuallyArgs...).Should(Succeed())
			}

			{
				By("Checking if Route was successfully created in the reconciliation")
				found := &routev1.Route{}
				Eventually(func() error {
					err := k8sClient.Get(ctx, nsName, found)
					if meta.IsNoMatchError(err) {
						Skip("cluster does not support route")
					}
					return err
				}, eventuallyArgs...).Should(Succeed())
				Expect(found.GetOwnerReferences()).To(ContainElement(ownerRef))
				//				Expect(found.Spec.To).To(Equal(nil), "route target doesn't match")
			}
		})
	})
})
