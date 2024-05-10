// Copyright: This file is part of korrel8r, released under https://github.com/korrel8r/korrel8r/blob/main/LICENSE
package controllers

import (
	"context"
	"log"
	"os"
	"path/filepath"
	"reflect"
	"testing"
	"time"

	korrel8rv1alpha1 "github.com/korrel8r/operator/api/v1alpha1"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/client-go/kubernetes/scheme"
	"k8s.io/client-go/tools/record"
	"k8s.io/utils/ptr"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/envtest"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"
)

const (
	name  = "test-korrel8r"
	image = "github.com/korrel8r/korrel8r:1.2.3"
)

var (
	c   client.Client
	ctx = context.Background()
)

// Setup/teardown for the test suite
func TestMain(m *testing.M) {
	check := func(err error) {
		if err != nil {
			log.Fatal(err)
		}
	}
	// Setup
	check(AddToScheme(scheme.Scheme))
	testEnv := &envtest.Environment{
		CRDDirectoryPaths:     []string{filepath.Join("..", "config", "crd", "bases")},
		ErrorIfCRDPathMissing: true,
	}
	cfg, err := testEnv.Start()
	check(err)
	defer func() { check(testEnv.Stop()) }()
	c, err = client.New(cfg, client.Options{})
	check(err)
	os.Exit(m.Run())
}

const (
	timeout = time.Second
	tick    = time.Second / 10
)

// setup creates a unique namespace
func setup(t *testing.T) *korrel8rv1alpha1.Korrel8r {
	t.Helper()
	ns := &corev1.Namespace{ObjectMeta: metav1.ObjectMeta{GenerateName: name}}
	require.NoError(t, c.Create(ctx, ns))
	t.Log("Namespace:", ns.Name)
	return &korrel8rv1alpha1.Korrel8r{ObjectMeta: metav1.ObjectMeta{Namespace: ns.Name, Name: name}}
}

func TestKorrel8rReconcilerReconcile_empty(t *testing.T) {
	korrel8r := setup(t)
	nsName := client.ObjectKeyFromObject(korrel8r)
	require.NoError(t, c.Create(ctx, korrel8r))
	gvk := schema.FromAPIVersionAndKind(korrel8rv1alpha1.GroupVersion.String(), reflect.TypeOf(korrel8r).Elem().Name())
	ownerRef := *metav1.NewControllerRef(korrel8r, gvk) // Expected owner ref
	korrel8rReconciler := NewKorrel8rReconciler(image, c, c.Scheme(), record.NewFakeRecorder(1000))
	result, err := korrel8rReconciler.Reconcile(ctx, reconcile.Request{NamespacedName: nsName})
	require.NoError(t, err)
	require.Zero(t, result)

	assertGetEventually := func(o client.Object) {
		t.Helper()
		assert.EventuallyWithT(t,
			func(t *assert.CollectT) { assert.NoError(t, c.Get(ctx, nsName, o)) },
			timeout, tick)
		assert.Contains(t, o.GetOwnerReferences(), ownerRef)
	}

	d := &appsv1.Deployment{}
	assertGetEventually(d)
	assert.Equal(t,
		map[string]string{
			"app.kubernetes.io/version":  "1.2.3",
			"app.kubernetes.io/instance": "test-korrel8r",
			"app.kubernetes.io/name":     "korrel8r",
		}, d.Spec.Selector.MatchLabels)

	svc := &corev1.Service{}
	assertGetEventually(svc)
	assert.Equal(t, d.Spec.Selector.MatchLabels, svc.Spec.Selector)

	// Wait for expected condition
	assert.EventuallyWithT(t,
		func(t *assert.CollectT) {
			require.NoError(t, c.Get(ctx, nsName, korrel8r))
			conditions := korrel8r.Status.Conditions
			require.Equal(t, 1, len(conditions))
			want := metav1.Condition{
				Type:    ConditionTypeAvailable,
				Status:  metav1.ConditionTrue,
				Reason:  "Reconciled",
				Message: "Ready",
			}
			got := conditions[0]
			// Only compare relevant fields, timestamp and generation fields will not match.
			assert.Equal(t, want.Type, got.Type)
			assert.Equal(t, want.Status, got.Status)
			assert.Equal(t, want.Reason, got.Reason)
			assert.Equal(t, want.Message, got.Message)
		},
		timeout, tick)
}

func TestKorrel8rReconcilerReconcile_configmap(t *testing.T) {
	korrel8r := setup(t)
	korrel8r.Spec.ConfigMap = &corev1.LocalObjectReference{Name: "foobar"}
	nsName := client.ObjectKeyFromObject(korrel8r)

	require.NoError(t, c.Create(ctx, korrel8r))

	korrel8rReconciler := NewKorrel8rReconciler(image, c, c.Scheme(), record.NewFakeRecorder(1000))
	result, err := korrel8rReconciler.Reconcile(ctx, reconcile.Request{NamespacedName: nsName})
	require.NoError(t, err)
	require.Zero(t, result)

	d := &appsv1.Deployment{}
	assert.EventuallyWithT(t,
		func(t *assert.CollectT) { assert.NoError(t, c.Get(ctx, nsName, d)) },
		timeout, tick)
	spec := d.Spec.Template.Spec
	assert.Contains(t, spec.Containers[0].Command, "--config=/config/korrel8r.yaml")
	assert.Contains(t, spec.Containers[0].VolumeMounts,
		corev1.VolumeMount{
			Name:      "config",
			ReadOnly:  true,
			MountPath: "/config"})
	assert.Contains(t, spec.Volumes,
		corev1.Volume{
			Name: "config",
			VolumeSource: corev1.VolumeSource{
				ConfigMap: &corev1.ConfigMapVolumeSource{
					LocalObjectReference: corev1.LocalObjectReference{Name: "foobar"},
					DefaultMode:          ptr.To(int32(0644)),
				}}})
}

func TestKorrel8rReconcilerReconcile_debug(t *testing.T) {
	korrel8r := setup(t)
	korrel8r.Spec.Debug = &korrel8rv1alpha1.DebugSpec{Verbose: 3}
	require.NoError(t, c.Create(ctx, korrel8r))
	nsName := client.ObjectKeyFromObject(korrel8r)

	korrel8rReconciler := NewKorrel8rReconciler(image, c, c.Scheme(), record.NewFakeRecorder(1000))
	result, err := korrel8rReconciler.Reconcile(ctx, reconcile.Request{NamespacedName: nsName})
	require.NoError(t, err)
	require.Zero(t, result)
	d := &appsv1.Deployment{}
	assert.EventuallyWithT(t,
		func(t *assert.CollectT) { assert.NoError(t, c.Get(ctx, nsName, d)) },
		timeout, tick)
	assert.Contains(t, d.Spec.Template.Spec.Containers[0].Env, corev1.EnvVar{Name: "KORREL8R_VERBOSE", Value: "3"})

}
