// Copyright: This file is part of korrel8r, released under https://github.com/korrel8r/korrel8r/blob/main/LICENSE

package controllers

import (
	"context"
	"crypto/md5"
	"errors"
	"fmt"
	"maps"
	"os"
	"strconv"
	"strings"

	"github.com/go-logr/logr"
	"github.com/google/go-cmp/cmp"
	korrel8rv1alpha1 "github.com/korrel8r/operator/api/v1alpha1"
	routev1 "github.com/openshift/api/route/v1"
	"gopkg.in/yaml.v2"
	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	rbacv1 "k8s.io/api/rbac/v1"
	"k8s.io/apimachinery/pkg/api/equality"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/util/intstr"
	utilruntime "k8s.io/apimachinery/pkg/util/runtime"
	"k8s.io/client-go/tools/record"
	"k8s.io/client-go/util/retry"
	"k8s.io/utils/ptr"
	"sigs.k8s.io/controller-runtime/pkg/builder"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"
	"sigs.k8s.io/controller-runtime/pkg/log"
	"sigs.k8s.io/controller-runtime/pkg/manager"
	"sigs.k8s.io/controller-runtime/pkg/predicate"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"
)

// List permissions needed by Reconcile - used to generate role.yaml
//
//+kubebuilder:rbac:groups=korrel8r.openshift.io,resources=korrel8rs,verbs=get;list;watch;create;update;patch;delete
//+kubebuilder:rbac:groups=korrel8r.openshift.io,resources=korrel8rs/status,verbs=get;update;patch
//+kubebuilder:rbac:groups=korrel8r.openshift.io,resources=korrel8rs/finalizers,verbs=update
//
//+kubebuilder:rbac:groups=core,resources=events,verbs=create;patch
//+kubebuilder:rbac:groups=core,resources=configmaps;services;serviceaccounts,verbs=get;list;watch;create;update;patch;delete
//+kubebuilder:rbac:groups=apps,resources=deployments,verbs=get;list;watch;create;update;patch;delete
//+kubebuilder:rbac:groups=core,resources=pods,verbs=get;list;watch
//+kubebuilder:rbac:groups=route.openshift.io,resources=routes,verbs=get;list;watch;create;update;patch;delete
//+kubebuilder:rbac:groups=rbac.authorization.k8s.io,resources=roles;rolebindings;clusterroles;clusterrolebindings,verbs=get;list;watch;create;update;patch;delete
//
// TODO restrict to read-only access. Restrict for normal users?
//+kubebuilder:rbac:groups=*,resources=*,verbs=*

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

// Korrel8rReconciler reconciles a Korrel8r object
type Korrel8rReconciler struct {
	client.Client
	Scheme   *runtime.Scheme
	Recorder record.EventRecorder
}

func NewKorrel8rReconciler(c client.Client, s *runtime.Scheme, e record.EventRecorder) *Korrel8rReconciler {
	return &Korrel8rReconciler{
		Client:   c,
		Scheme:   s,
		Recorder: e,
	}
}

func (r *Korrel8rReconciler) SetupWithManager(mgr manager.Manager) error {
	return builder.ControllerManagedBy(mgr).
		For(&korrel8rv1alpha1.Korrel8r{}).
		Owns(&corev1.ConfigMap{}).
		Owns(&appsv1.Deployment{}). // After configmap, needs configHash
		Owns(&corev1.Service{}).
		Owns(&corev1.ServiceAccount{}).
		Owns(&routev1.Route{}).
		// Only process updates that modify spec or labels.
		WithEventFilter(predicate.Or(predicate.GenerationChangedPredicate{}, predicate.LabelChangedPredicate{})).
		WithLogConstructor(func(rr *reconcile.Request) logr.Logger { // Default is has too many redundant fields.
			if rr != nil {
				gvk, _ := r.GroupVersionKindFor(&korrel8rv1alpha1.Korrel8r{})
				return mgr.GetLogger().WithValues(gvk.Kind, rr.String())
			}
			return mgr.GetLogger()
		}).
		Complete(r)
}

// requestContext computed for each call to Reconcile
type requestContext struct {
	*Korrel8rReconciler // Parent reconciler

	// Main resource
	Korrel8r korrel8rv1alpha1.Korrel8r
	// Owned resources.
	Deployment         appsv1.Deployment
	ConfigMap          corev1.ConfigMap
	Service            corev1.Service
	ServiceAccount     corev1.ServiceAccount
	Route              routev1.Route
	ClusterRole        rbacv1.ClusterRole
	ClusterRoleBinding rbacv1.ClusterRoleBinding

	// Context
	ctx context.Context
	req reconcile.Request
	log logr.Logger

	// Values computed per-reconcile
	image      string // korrel8r image
	selector   map[string]string
	labels     map[string]string
	configHash string // Computed by configMap, set on deployment.
}

func (r *Korrel8rReconciler) newRequestContext(ctx context.Context, req reconcile.Request) *requestContext {
	selector := map[string]string{ // Immutable selector labels
		"app.kubernetes.io/name":      ApplicationName,
		"app.kubernetes.io/component": ComponentName,
		"app.kubernetes.io/instance":  req.Namespace + "." + req.Name, // Use "namespace.name" as unique id
	}
	image := os.Getenv(ImageEnv)
	if image == "" {
		panic(fmt.Errorf("Environment variable %v must be set", ImageEnv))
	}
	_, version, _ := strings.Cut(image, ":")
	labels := map[string]string{ // Mutable non-selector lables, value may on update.
		"app.kubernetes.io/version": version,
	}
	maps.Copy(labels, selector) // Labels includes selector labels

	rc := &requestContext{
		Korrel8rReconciler: r,

		ctx: ctx,
		req: req,
		log: log.FromContext(ctx),

		image:    image,
		selector: selector,
		labels:   labels,
	}

	// Set name and namespace for owned resources
	for _, o := range []client.Object{&rc.ServiceAccount, &rc.ConfigMap, &rc.Deployment, &rc.Service, &rc.Route} {
		o.SetNamespace(req.Namespace)
		o.SetName(req.Name)
	}
	for _, o := range []client.Object{&rc.ClusterRole, &rc.ClusterRoleBinding} {
		o.SetName(RoleName)
	}
	return rc
}

// Reconcile is the main reconcile loop
func (r *Korrel8rReconciler) Reconcile(ctx context.Context, req reconcile.Request) (result reconcile.Result, err error) {
	rc := r.newRequestContext(ctx, req)
	log := rc.log

	// Always log result on return
	defer func() {
		if err != nil {
			log.Error(err, "Reconcile error", "requeue", !result.IsZero())
		} else {
			log.Info("Reconcile succeeded", "requeue", !result.IsZero())
		}
	}()

	if err = r.Get(ctx, req.NamespacedName, &rc.Korrel8r); err != nil {
		if apierrors.IsNotFound(err) {
			log.Info("Requested resource not found")
			return result, nil
		}
		return result, err
	}

	korrel8rBefore := rc.Korrel8r.DeepCopy() // Save the initial state.

	// Update status condition on return.
	defer func() {
		cond := conditionFor(err)
		if meta.SetStatusCondition(&rc.Korrel8r.Status.Conditions, cond) { // Condition changed
			err = errors.Join(err, r.Status().Update(ctx, &rc.Korrel8r))
			if err != nil {
				r.Recorder.Eventf(&rc.Korrel8r, corev1.EventTypeWarning, ReasonReconciling, "Reconcile error: %v", err)
			} else {
				r.Recorder.Eventf(&rc.Korrel8r, corev1.EventTypeNormal, ReasonReconciled, "Reconcile succeeded")
			}
		}
		rc.logChange(controllerutil.OperationResultUpdated, korrel8rBefore, &rc.Korrel8r)
	}()

	// Create or update each owned resource
	for _, f := range []func() error{
		rc.serviceAccount,
		rc.configMap,
		rc.deployment,
		rc.service,
		rc.clusterRole,
		rc.clusterRoleBinding,
		rc.route,
	} {
		if errors.Is(err, errReconcileAgain{}) { // Re-start the reconcile.
			return reconcile.Result{Requeue: true}, nil
		}
		err = errors.Join(err, retry.RetryOnConflict(retry.DefaultRetry, f))
	}
	return result, errors.Join(err, r.Status().Update(ctx, &rc.Korrel8r))
}

func conditionFor(err error) metav1.Condition {
	if err == nil {
		return metav1.Condition{
			Type:    ConditionTypeAvailable,
			Status:  metav1.ConditionTrue,
			Reason:  ReasonReconciled,
			Message: "Ready",
		}
	}
	return metav1.Condition{
		Type:    ConditionTypeAvailable,
		Status:  metav1.ConditionFalse,
		Reason:  ReasonReconciling,
		Message: err.Error(),
	}
}

// logChange logs OperationResults (updates and creattes) for debugging.
func (r *requestContext) logChange(op controllerutil.OperationResult, before, after client.Object) {
	if op != controllerutil.OperationResultNone && !equality.Semantic.DeepEqual(before, after) {
		gvk, _ := r.Client.GroupVersionKindFor(after)
		r.log.V(1).Info("Modified by reconcile", "kind", gvk.Kind, "name", after.GetName(), "operation", op)
		if r.log.V(2).Enabled() { // Dump diff as raw text
			fmt.Fprintln(os.Stderr, cmp.Diff(before, after))
		}
	}
}

// createOrUpdate wraps controllerutil.CreateOrUpdate to do common setup and error handling/logging.
func (r *requestContext) createOrUpdate(o client.Object, mutate func() error) error {
	var before client.Object
	op, err := controllerutil.CreateOrUpdate(r.ctx, r.Client, o, func() error {
		before = o.DeepCopyObject().(client.Object)
		// Common settings for all objects
		o.SetLabels(r.labels)
		if o.GetNamespace() == r.Korrel8r.GetNamespace() {
			utilruntime.Must(controllerutil.SetControllerReference(&r.Korrel8r, o, r.Scheme))
		}
		return mutate()
	})
	if err == nil {
		r.logChange(op, before, o)
	}
	return err
}

func (r *requestContext) configMap() error {
	cm := &r.ConfigMap
	return r.createOrUpdate(cm, func() error {
		config := r.Korrel8r.Spec.Config
		if config == nil { // Default configuration
			config = &korrel8rv1alpha1.Config{
				// FIXME should  Default to openshift-internal configuration with all rules enabled.
				Include: []string{"/etc/korrel8r/stores/openshift-external.yaml", "/etc/korrel8r/rules/all.yaml"},
			}
		}
		conf, err := yaml.Marshal(config)
		if err != nil {
			return fmt.Errorf("invalid korrel8r configuration: %w", err)
		}
		if cm.Data == nil {
			cm.Data = map[string]string{}
		}
		// Only reconcile the main config-key entry, allow user to add others.
		cm.Data[ConfigKey] = string(conf)
		// Save the config hash for the deployment.
		r.configHash = fmt.Sprintf("%x", md5.Sum(conf))
		return nil
	})
}

type errReconcileAgain struct{}

func (e errReconcileAgain) Error() string { return "Need to re-run reconciliation." }

func (r *requestContext) deployment() error {
	d := &r.Deployment
	return r.createOrUpdate(d, func() error {
		wantSelector := &metav1.LabelSelector{MatchLabels: r.selector}
		if d.ObjectMeta.CreationTimestamp.IsZero() { // Selector is immutable, set on create only.
			d.Spec.Selector = wantSelector
		}
		if !equality.Semantic.DeepEqual(d.Spec.Selector, wantSelector) {
			r.log.Info("Removing deployment with invalid selector", "selector", d.Spec.Selector)
			if err := r.Delete(r.ctx, d); err != nil {
				return err
			}
			return errReconcileAgain{}
		}
		d.Spec.Template = corev1.PodTemplateSpec{
			ObjectMeta: metav1.ObjectMeta{Labels: r.labels},
			Spec: corev1.PodSpec{
				Containers: []corev1.Container{
					{
						Name:    ApplicationName,
						Image:   r.image,
						Command: []string{"korrel8r", "web", "--https", ":8443", "--cert", "/secrets/tls.crt", "--key", "/secrets/tls.key", "--config", "/config/korrel8r.yaml"},
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
				ServiceAccountName: r.Korrel8r.Name,
				Volumes: []corev1.Volume{
					{
						Name: "config",
						VolumeSource: corev1.VolumeSource{
							ConfigMap: &corev1.ConfigMapVolumeSource{
								LocalObjectReference: corev1.LocalObjectReference{Name: r.Korrel8r.Name},
							}},
					},
					{
						Name:         "secrets",
						VolumeSource: corev1.VolumeSource{Secret: &corev1.SecretVolumeSource{SecretName: r.Korrel8r.Name}},
					},
				},
			},
		}
		env := &d.Spec.Template.Spec.Containers[0].Env
		if r.configHash != "" {
			*env = append(*env, corev1.EnvVar{Name: "CONFIG_HASH", Value: r.configHash}) // To force update if configmap changes.
		}
		if r.Korrel8r.Spec.Verbose > 0 { //  Set KORREL8R_VERBOSE environment variable
			*env = append(*env, corev1.EnvVar{Name: VerboseEnv, Value: strconv.Itoa(r.Korrel8r.Spec.Verbose)})
		}
		if r.Korrel8r.Spec.Verbose >= 3 { // Always pull images if verbose is set for debugging
			d.Spec.Template.Spec.Containers[0].ImagePullPolicy = corev1.PullAlways
		}
		return nil
	})
}

func (r *requestContext) service() error {
	s := &r.Service
	return r.createOrUpdate(s, func() error {
		port := intstr.FromInt(8443)
		s.ObjectMeta.Annotations = map[string]string{
			// Generate a TLS server certificate in a secret
			"service.beta.openshift.io/serving-cert-secret-name": r.Korrel8r.Name,
		}
		s.Spec = corev1.ServiceSpec{
			Selector: r.selector,
			Ports: []corev1.ServicePort{
				{
					Name:       "web",
					Port:       port.IntVal,
					Protocol:   corev1.ProtocolTCP,
					TargetPort: port,
				},
			},
		}
		return nil
	})
}

func (r *requestContext) route() error {
	route := &r.Route
	err := r.createOrUpdate(route, func() error {
		route.Spec.TLS = &routev1.TLSConfig{Termination: routev1.TLSTerminationPassthrough}
		route.Spec.To = routev1.RouteTargetReference{
			Kind: "Service",
			Name: r.Korrel8r.Name,
		}
		return nil
	})
	if meta.IsNoMatchError(err) { // Route is optional, no error if unavailable.
		gvk, _ := r.Client.GroupVersionKindFor(route)
		r.log.Info("Skip unsupported kind", "kind", gvk.GroupKind()) // Not an error
		return nil
	}
	return err
}

func (r *requestContext) serviceAccount() error {
	sa := &r.ServiceAccount
	return r.createOrUpdate(sa, func() error {
		return nil
	})
}

func (r *requestContext) clusterRole() error {
	role := &r.ClusterRole
	return r.createOrUpdate(role, func() error {
		// Read-only access to everything
		// TODO This needs review for release, allow more restricted options?
		role.Rules = []rbacv1.PolicyRule{
			{
				APIGroups: []string{"*"},
				Resources: []string{"*"},
				Verbs:     []string{"get", "watch", "list"},
			},
		}
		return nil
	})
}

func (r *requestContext) clusterRoleBinding() error {
	binding := &r.ClusterRoleBinding
	return r.createOrUpdate(binding, func() error {
		binding.RoleRef = rbacv1.RoleRef{
			APIGroup: rbacv1.SchemeGroupVersion.Group,
			Kind:     "ClusterRole",
			Name:     "cluster-admin",
		}
		binding.Subjects = []rbacv1.Subject{
			{
				Kind:      rbacv1.ServiceAccountKind,
				Name:      r.Korrel8r.Name,
				Namespace: r.Korrel8r.Namespace,
			},
		}
		return nil
	})
}

// TODO Lifecycle of clusterrole and clusterrolebinding:
// - remove subject from CRB when Korrel8r instance is deleted.
// - no need to remove role/binding when all are deleted?
// Note this is thet same problem as CLO. Should korrel8r be a cluster-scoped operator?
