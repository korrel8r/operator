// Copyright: This file is part of korrel8r, released under https://github.com/korrel8r/korrel8r/blob/main/LICENSE

package controllers

import (
	"context"
	"errors"
	"fmt"
	"os"
	"reflect"
	"strconv"
	"strings"

	"gopkg.in/yaml.v2"
	appsv1 "k8s.io/api/apps/v1"
	rbacv1 "k8s.io/api/rbac/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/meta"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/apimachinery/pkg/util/intstr"
	"k8s.io/client-go/tools/record"
	"k8s.io/client-go/util/retry"
	"k8s.io/utils/ptr"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/apiutil"
	"sigs.k8s.io/controller-runtime/pkg/log"
	"sigs.k8s.io/controller-runtime/pkg/predicate"

	"github.com/go-logr/logr"
	korrel8r "github.com/korrel8r/operator/api/v1alpha1"
	"golang.org/x/exp/maps"
	"golang.org/x/exp/slices"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	v1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

const (
	ApplicationName        = "korrel8r"
	ComponentName          = "engine"
	RoleName               = ApplicationName + "-" + ComponentName + "-reader"
	ConfigKey              = "korrel8r.yaml" // ConfigKey for the root configuration in ConfigMap.Data.
	ImageEnv               = "KORREL8R_IMAGE"
	VerboseEnv             = "KORREL8R_VERBOSE"
	ConditionTypeAvailable = "Available"
	ConditionTypeDegraded  = "Degraded"
	ReasonReconciling      = "Reconciling"
	ReasonReconciled       = "Reconciled"
)

// List permissions needed by Reconcile - used to generate role.yaml
//
//+kubebuilder:rbac:groups=korrel8r.openshift.io,resources=korrel8rs,verbs=get;list;watch;create;update;patch;delete
//+kubebuilder:rbac:groups=korrel8r.openshift.io,resources=korrel8rs/status,verbs=get;update;patch
//+kubebuilder:rbac:groups=korrel8r.openshift.io,resources=korrel8rs/finalizers,verbs=update
//
//+kubebuilder:rbac:groups=core,resources=events,verbs=create;patch
//+kubebuilder:rbac:groups=core,resources=;configmaps;services;serviceaccounts,verbs=get;list;watch;create;update;patch;delete
//+kubebuilder:rbac:groups=apps,resources=deployments,verbs=get;list;watch;create;update;patch;delete
//+kubebuilder:rbac:groups=core,resources=pods,verbs=get;list;watch

// Korrel8rReconciler reconciles a Korrel8r object
type Korrel8rReconciler struct {
	client.Client
	Scheme   *runtime.Scheme
	Recorder record.EventRecorder
}

// FIXME Ownership of cluster role and binding. Currently we will create/reconcile but we don't watch/own.

// SetupWithManager sets up the controller with the Manager.
func (r *Korrel8rReconciler) SetupWithManager(mgr ctrl.Manager) error {
	return ctrl.NewControllerManagedBy(mgr).
		For(&korrel8r.Korrel8r{}).
		Owns(&appsv1.Deployment{}).
		Owns(&corev1.ConfigMap{}).
		Owns(&corev1.Service{}).
		Owns(&corev1.ServiceAccount{}).
		// Ignore updates that don't modify spec or labels.
		WithEventFilter(predicate.Or(predicate.GenerationChangedPredicate{}, predicate.LabelChangedPredicate{})).
		Complete(r)
}

// Reconcile is the main reconcile loop
func (r *Korrel8rReconciler) Reconcile(ctx context.Context, req ctrl.Request) (result ctrl.Result, err error) {
	rc, err := r.newRequestContext(ctx, req)
	if err != nil {
		return result, err
	}
	defer func() { rc.log.Info("Reconciled", "error", err, "created", rc.created, "updated", rc.updated) }()

	err = r.Get(ctx, req.NamespacedName, &rc.korrel8r)
	if apierrors.IsNotFound(err) {
		rc.log.Info("Resource not found") // Nothing to do.
		return result, nil
	} else if err != nil {
		return result, err
	}

	// Update condition on return from reconcile.
	defer func() {
		rc.log.Info("Reconciled", "error", err, "created", rc.created, "updated", rc.updated)
		// Create the new condition
		condition := metav1.Condition{
			Type:    ConditionTypeAvailable,
			Status:  metav1.ConditionTrue,
			Reason:  ReasonReconciled,
			Message: "Ready",
		}
		if err != nil {
			condition = metav1.Condition{
				Type:    ConditionTypeAvailable,
				Status:  metav1.ConditionFalse,
				Reason:  ReasonReconciling,
				Message: err.Error(),
			}
		}
		// Only record an event on status changes.
		if r.Recorder != nil && !meta.IsStatusConditionPresentAndEqual(rc.korrel8r.Status.Conditions, condition.Type, condition.Status) {
			if err != nil {
				r.Recorder.Eventf(&rc.korrel8r, corev1.EventTypeWarning, ReasonReconciling, "Reconcile error: %v", err)
			} else {
				r.Recorder.Eventf(&rc.korrel8r, corev1.EventTypeNormal, ReasonReconciled, "Reconcile succeeded: created: %v updated: %v", rc.created, rc.updated)
			}
		}
		// FIXME always update the condition? If not we don't get re-reconciled?
		meta.SetStatusCondition(&rc.korrel8r.Status.Conditions, condition)
		err2 := r.Status().Update(ctx, &rc.korrel8r)
		if err == nil { // Don't over-write the original error
			err = err2
		}
	}()

	// Create or update each owned resource
	nn := req.NamespacedName
	roleNN := types.NamespacedName{Name: RoleName}
	for _, f := range []func() error{
		func() error { return rc.modifyKorrel8r() },
		func() error { return createOrUpdate(rc, nn, rc.serviceAccount) },
		func() error { return createOrUpdate(rc, nn, rc.configMap) },
		func() error { return createOrUpdate(rc, nn, rc.deployment) },
		func() error { return createOrUpdate(rc, nn, rc.service) },
		func() error { return createOrUpdate(rc, roleNN, rc.clusterRole) },
		func() error { return createOrUpdate(rc, roleNN, rc.clusterRoleBinding) },
	} {
		if err := f(); err != nil {
			return result, err
		}
	}
	return result, errors.Join(err, r.Status().Update(ctx, &rc.korrel8r))
}

func (r *Korrel8rReconciler) newRequestContext(ctx context.Context, req ctrl.Request) (*requestContext, error) {
	rc := &requestContext{
		Korrel8rReconciler: r,
		ctx:                ctx,
		log:                log.FromContext(ctx),
		image:              os.Getenv(ImageEnv),
		// Note: selector labels are immutable, they must not change if the image is updated
		selector: map[string]string{
			"app.kubernetes.io/name":      ApplicationName,
			"app.kubernetes.io/component": ComponentName,
			"app.kubernetes.io/instance":  req.Namespace + "." + req.Name, // Use "namespace.name" as unique id
		},
		labels: map[string]string{},
	}
	if rc.image == "" {
		return nil, fmt.Errorf("environment variable not set: %v", ImageEnv)
	}
	// Note: labels are not immutable, so may include values that change during update.
	_, version, _ := strings.Cut(rc.image, ":")
	maps.Copy(rc.labels, rc.selector)
	rc.labels["app.kubernetes.io/version"] = version
	return rc, nil
}

// requestContext computed for each call to Reconcile
type requestContext struct {
	*Korrel8rReconciler
	ctx      context.Context
	log      logr.Logger
	image    string
	selector map[string]string
	labels   map[string]string
	korrel8r korrel8r.Korrel8r

	created, updated []string
}

func (r *requestContext) modifyKorrel8r() error {
	// Fill in default if configuration is missing.
	if r.korrel8r.Spec.Config == nil {
		r.korrel8r.Spec.Config = &korrel8r.Config{
			// Default to openshift-internal configuration with all rules enabled.
			Include: []string{"/etc/korrel8r/stores/openshift-internal.yaml", "/etc/korrel8r/rules/all.yaml"},
		}
		r.updated = append(r.updated, r.kindOf(&r.korrel8r))
		if err := r.Update(r.ctx, &r.korrel8r); err != nil {
			return err
		}
	}
	return nil
}

func (r *requestContext) configMap(configMap *corev1.ConfigMap) (bool, error) {
	// Extract config file from korrel8r resource.
	conf, err := yaml.Marshal(r.korrel8r.Spec.Config)
	if err != nil {
		return false, fmt.Errorf("invalid korrel8r configuration: %w", err)
	}
	if configMap.Data == nil {
		configMap.Data = map[string]string{}
	}
	// Only reconcile the main config-key entry, allow user to add others.
	if string(conf) != configMap.Data[ConfigKey] {
		configMap.Data[ConfigKey] = string(conf)
		return true, nil
	}
	return false, nil
}

func (r *requestContext) deployment(got *appsv1.Deployment) (bool, error) {
	want := appsv1.Deployment{
		Spec: appsv1.DeploymentSpec{
			Selector: &metav1.LabelSelector{MatchLabels: r.selector},
			Template: corev1.PodTemplateSpec{
				ObjectMeta: metav1.ObjectMeta{
					Labels: r.labels,
				},
				Spec: corev1.PodSpec{
					Containers: []corev1.Container{
						{
							Name:    ApplicationName,
							Image:   r.image,
							Command: []string{"korrel8r", "web", "--https", ":8443", "--cert", "/secrets/tls.crt", "--key", "/secrets/tls.key"},
							VolumeMounts: []corev1.VolumeMount{
								{
									Name:      "secrets",
									ReadOnly:  true,
									MountPath: "/secrets",
								},
								{
									Name:      "config",
									ReadOnly:  true,
									MountPath: "/config",
								},
							},
							Ports: []corev1.ContainerPort{
								{ContainerPort: 8443, Protocol: corev1.ProtocolTCP},
							},
							SecurityContext: &corev1.SecurityContext{
								AllowPrivilegeEscalation: ptr.To(false),
								Capabilities: &corev1.Capabilities{
									Drop: []corev1.Capability{"ALL"},
								},
								RunAsNonRoot: ptr.To(true),
								SeccompProfile: &corev1.SeccompProfile{
									Type: corev1.SeccompProfileTypeRuntimeDefault,
								},
							},
						},
					},
					ServiceAccountName: r.korrel8r.Name,
					Volumes: []corev1.Volume{
						{
							Name: "config",
							VolumeSource: corev1.VolumeSource{
								ConfigMap: &corev1.ConfigMapVolumeSource{
									LocalObjectReference: corev1.LocalObjectReference{Name: r.korrel8r.Name},
								}},
						},
						{
							Name:         "secrets",
							VolumeSource: corev1.VolumeSource{Secret: &corev1.SecretVolumeSource{SecretName: r.korrel8r.Name}},
						},
					},
				},
			},
		},
	}
	if r.korrel8r.Spec.Verbose > 0 { //  Set KORREL8R_VERBOSE environment variable
		want.Spec.Template.Spec.Containers[0].Env = []corev1.EnvVar{
			{Name: VerboseEnv, Value: strconv.Itoa(r.korrel8r.Spec.Verbose)},
		}
	}
	if r.korrel8r.Spec.Verbose >= 3 { // Always pull images if verbose is set for debugging
		want.Spec.Template.Spec.Containers[0].ImagePullPolicy = corev1.PullAlways
	}
	changed := anyTrue(
		update(r, &want.Spec.Selector, &got.Spec.Selector, "Updated deployment selector"),
		update(r, &want.Spec.Template.Spec.ServiceAccountName, &got.Spec.Template.Spec.ServiceAccountName, "Updated deployment service account"),
	)
	if len(got.Spec.Template.Spec.Containers) != 1 {
		changed = true
		got.Spec.Template = want.Spec.Template
	} else {
		wantC := &want.Spec.Template.Spec.Containers[0]
		gotC := &got.Spec.Template.Spec.Containers[0]
		changed = anyTrue(
			update(r, &wantC.Command, &gotC.Command, "Updated deployment command"),
			update(r, &wantC.Env, &gotC.Env, "Updated deployment env"),
			update(r, &wantC.Image, &gotC.Image, "Updated deployment image"),
			update(r, &wantC.ImagePullPolicy, &gotC.ImagePullPolicy, "Updated deployment pull policy"),
			update(r, &wantC.Name, &gotC.Name, "Updated deployment container name"),
			update(r, &wantC.Ports, &gotC.Ports, "Updated deployment ports"),
			update(r, &wantC.VolumeMounts, &gotC.VolumeMounts, "Updated deployment volume mounts"),
		) || changed
	}
	return changed, nil
}

func (r *requestContext) service(got *corev1.Service) (bool, error) {
	port := intstr.FromInt(8443)
	want := &corev1.Service{
		ObjectMeta: v1.ObjectMeta{
			Annotations: map[string]string{
				// Generate a TLS server certificate in a secret
				"service.beta.openshift.io/serving-cert-secret-name": r.korrel8r.Name,
			}},
		Spec: corev1.ServiceSpec{
			Selector: r.selector,
			Ports: []corev1.ServicePort{
				{Name: "web", Port: port.IntVal, Protocol: corev1.ProtocolTCP, TargetPort: port},
			},
		},
	}
	return anyTrue(
		update(r, &want.Spec.Ports, &got.Spec.Ports, "Updated service ports"),
		update(r, &want.Spec.Selector, &got.Spec.Selector, "Updated service selector"),
		update(r, &want.ObjectMeta.Annotations, &got.ObjectMeta.Annotations, "Updated service annotations"),
	), nil
}

func (r *requestContext) serviceAccount(serviceAccount *corev1.ServiceAccount) (bool, error) {
	return false, nil // Nothing to validate except existence
}

func (r *requestContext) clusterRole(got *rbacv1.ClusterRole) (bool, error) {
	// Read-only access to everything
	// TODO This needs review for release, allow more restricted options?
	want := &rbacv1.ClusterRole{
		Rules: []rbacv1.PolicyRule{
			{
				APIGroups: []string{"*"},
				Resources: []string{"*"},
				Verbs:     []string{"get", "watch", "list"},
			},
		},
	}
	return update(r, &want.Rules, &got.Rules, "Updated clusterrole rules"), nil
}

// FIXME revisit number, name and ownership of role bindings. Finalizer for CRB?
func (r *requestContext) clusterRoleBinding(got *rbacv1.ClusterRoleBinding) (bool, error) {
	want := &rbacv1.ClusterRoleBinding{
		RoleRef: rbacv1.RoleRef{
			APIGroup: rbacv1.SchemeGroupVersion.Group,
			Kind:     "ClusterRole",
			Name:     "cluster-admin",
		},
		Subjects: []rbacv1.Subject{
			{
				Kind:      rbacv1.ServiceAccountKind,
				Name:      r.korrel8r.Name,
				Namespace: r.korrel8r.Namespace,
			},
		},
	}
	changed := update(r, &want.RoleRef, &got.RoleRef, "Updated role reference for cluster role binding.")
	i := slices.Index(got.Subjects, want.Subjects[0]) // Find the subject, add if missing
	if i == -1 {
		i = len(got.Subjects)
		got.Subjects = append(got.Subjects, rbacv1.Subject{})
	}
	changed = update(r, &want.Subjects[0], &got.Subjects[i], "Updated subjects for cluster role binding") || changed
	return changed, nil
}

// FIXME Lifecycle of clusterrole and clusterrolebinding:
// - remove subject from CRB when Korrel8r instance is deleted.
// - no need to remove role/binding when all are deleted?

// update modifies expected if different from actual, and returns true if it was modified.
func update[T any](r *requestContext, expected, actual *T, msg string) (changed bool) {
	equal := reflect.DeepEqual(*expected, *actual)
	if !equal {
		*actual = *expected
		r.log.V(2).Info(msg)
	}
	return !equal
}

// anyTrue returns true if any of its parameters are true
func anyTrue(values ...bool) bool { return slices.Index(values, true) != -1 }

// Type constraint to ensure *T is a client.Object
type objectPtr[T any] interface {
	*T
	client.Object
}

// createOrUpdate does a GET of name into o, then calls the update() function to update it if it does not match the spec.
// update() returns true or false to indicate if the object was modified.
func createOrUpdate[T any, PT objectPtr[T]](r *requestContext, nn types.NamespacedName, reconcile func(*T) (bool, error)) (err error) {
	var t T
	o := PT(&t)
	err = r.Get(r.ctx, nn, o)
	notFound := apierrors.IsNotFound(err)
	if err != nil && !notFound {
		return err
	}
	changed, err := reconcile((*T)(o))
	if err != nil {
		return err
	}
	if !reflect.DeepEqual(o.GetLabels(), r.labels) { // Check for label changes.
		changed = true
		o.SetLabels(r.labels)
	}
	if notFound { // Create new
		r.created = append(r.created, r.kindOf(o))
		o.SetNamespace(nn.Namespace)
		o.SetName(nn.Name)
		ctrl.SetControllerReference(&r.korrel8r, o, r.Scheme)
		return r.Create(r.ctx, o)
	} else if changed { // Update existing
		r.updated = append(r.updated, r.kindOf(o))
		return retry.RetryOnConflict(retry.DefaultRetry, func() error { return r.Update(r.ctx, o) })
	}
	return nil
}

func (r *Korrel8rReconciler) kindOf(o client.Object) string {
	gvk, _ := apiutil.GVKForObject(o, r.Scheme)
	return gvk.Kind
}
