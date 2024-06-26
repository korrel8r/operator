// Copyright: This file is part of korrel8r, released under https://github.com/korrel8r/korrel8r/blob/main/LICENSE

package controllers

import (
	"context"
	"fmt"
	"maps"
	"os"
	"strconv"
	"strings"

	"slices"

	"github.com/go-logr/logr"
	"github.com/google/go-cmp/cmp"
	korrel8rv1alpha1 "github.com/korrel8r/operator/api/v1alpha1"
	routev1 "github.com/openshift/api/route/v1"
	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/equality"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/util/intstr"
	utilruntime "k8s.io/apimachinery/pkg/util/runtime"
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
//+kubebuilder:rbac:groups=core,resources=services,verbs=get;list;watch;create;update;patch;delete
//+kubebuilder:rbac:groups=apps,resources=deployments,verbs=get;list;watch;create;update;patch;delete
//+kubebuilder:rbac:groups=core,resources=pods,verbs=get;list;watch
//+kubebuilder:rbac:groups=route.openshift.io,resources=routes,verbs=get;list;watch;create;update;patch;delete
//

const (
	ApplicationName = "korrel8r"
	ConfigKey       = "korrel8r.yaml" // ConfigKey for the root configuration in ConfigMap.Data.
	ImageEnv        = "KORREL8R_IMAGE"
	VerboseEnv      = "KORREL8R_VERBOSE"

	ConditionTypeAvailable = "Available"
	ConditionTypeDegraded  = "Degraded"

	ReasonReconciling = "Reconciling"
	ReasonReconciled  = "Reconciled"

	// K8s recommended label names
	App         = "app.kubernetes.io/"
	AppInstance = App + "instance"
	AppName     = App + "name"
	AppVersion  = App + "version"
)

var (
	// Static labels applied to all owned objects.
	CommonLabels = map[string]string{AppName: ApplicationName}
)

// Korrel8rReconciler reconciles a Korrel8r object
type Korrel8rReconciler struct {
	client.Client
	Scheme  *runtime.Scheme
	image   string
	version string
}

func NewKorrel8rReconciler(image string, c client.Client, s *runtime.Scheme) *Korrel8rReconciler {
	_, version, _ := strings.Cut(image, ":")
	return &Korrel8rReconciler{
		Client:  c,
		Scheme:  s,
		image:   image,
		version: version,
	}
}

// Owned resources created by this operator
type Owned struct {
	Deployment appsv1.Deployment
	Route      routev1.Route
	Service    corev1.Service
}

func (o *Owned) each(f func(o client.Object)) {
	for _, obj := range []client.Object{&o.Deployment, &o.Service, &o.Route} {
		f(obj)
	}
}

func (r *Korrel8rReconciler) SetupWithManager(mgr manager.Manager) error {
	b := builder.ControllerManagedBy(mgr).
		For(&korrel8rv1alpha1.Korrel8r{}).
		WithEventFilter(predicate.Or(predicate.GenerationChangedPredicate{}, predicate.LabelChangedPredicate{})).
		WithLogConstructor(func(rr *reconcile.Request) logr.Logger { return mgr.GetLogger() })
	(&Owned{}).each(func(o client.Object) { b.Owns(o) })
	return b.Complete(r)
}

// requestContext computed for each call to Reconcile
type requestContext struct {
	*Korrel8rReconciler                           // Parent
	Korrel8r            korrel8rv1alpha1.Korrel8r // Main resource
	Owned                                         // Owned resources.

	// Context
	reconcile.Request
	ctx context.Context
	log logr.Logger

	// Values computed per-reconcile
	labels   map[string]string
	selector metav1.LabelSelector
}

func (r *Korrel8rReconciler) newRequestContext(ctx context.Context, req reconcile.Request) *requestContext {
	rc := &requestContext{
		Korrel8rReconciler: r,
		ctx:                ctx,
		Request:            req,
		log:                log.FromContext(ctx),
		labels:             maps.Clone(CommonLabels),
	}
	instance := req.Name
	rc.labels[AppInstance] = instance
	rc.labels[AppVersion] = rc.version
	rc.selector.MatchLabels = maps.Clone(rc.labels)
	rc.Owned.each(func(o client.Object) {
		o.SetNamespace(req.Namespace)
		o.SetName(req.Name)
	})
	return rc
}

// Reconcile is the main reconcile loop
func (r *Korrel8rReconciler) Reconcile(ctx context.Context, req reconcile.Request) (result reconcile.Result, err error) {
	rc := r.newRequestContext(ctx, req)
	log := rc.log
	log.Info("Reconcile begin")
	defer func() { log.Info("Reconcile end", "result", result, "error", err) }()

	if err = r.Get(ctx, req.NamespacedName, &rc.Korrel8r); err != nil {
		if apierrors.IsNotFound(err) {
			log.Info("Requested resource not found")
			return reconcile.Result{}, nil
		}
		return reconcile.Result{}, err
	}
	korrel8rBefore := rc.Korrel8r.DeepCopy() // Save the initial state.

	// Update status condition on return.
	defer func() {
		cond := errorCondition(err)
		if meta.SetStatusCondition(&rc.Korrel8r.Status.Conditions, cond) { // Condition changed
			log.Info("Updating status", "reason", cond.Reason, "message", cond.Message)
			if err2 := r.Status().Update(ctx, &rc.Korrel8r); err2 != nil {
				log.Error(err2, "Status update failed")
				err = err2
			}
		}
		rc.logChange(controllerutil.OperationResultUpdated, korrel8rBefore, &rc.Korrel8r)
	}()

	// Create or update each owned resource
	for _, f := range []func() error{
		rc.deployment,
		rc.service,
		rc.route,
	} {
		if err := retry.RetryOnConflict(retry.DefaultRetry, f); err != nil {
			return reconcile.Result{}, err
		}
	}
	return reconcile.Result{}, nil
}

func errorCondition(err error) metav1.Condition {
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

func (rc *requestContext) logDiff(before, after any) {
	if rc.log.V(2).Enabled() { // Dump diff as raw text
		fmt.Fprintln(os.Stderr, cmp.Diff(before, after))
	}
}

// logChange logs OperationResults (updates and creates) for debugging.
func (rc *requestContext) logChange(op controllerutil.OperationResult, before, after client.Object) {
	if op != controllerutil.OperationResultNone && !equality.Semantic.DeepEqual(before, after) {
		rc.log.V(1).Info("Modified", "kind", rc.kindOf(after), "name", after.GetName(), "operation", op)
		if op != controllerutil.OperationResultCreated {
			rc.logDiff(before, after)
		}
	}
}

// createOrUpdate wraps controllerutil.CreateOrUpdate to do common setup and error handling/logging.
func (rc *requestContext) createOrUpdate(o client.Object, mutate func() error) error {
	var before client.Object
	op, err := controllerutil.CreateOrUpdate(rc.ctx, rc.Client, o, func() error {
		before = o.DeepCopyObject().(client.Object)
		// Common settings for all objects
		o.SetLabels(rc.labels)
		utilruntime.Must(controllerutil.SetControllerReference(&rc.Korrel8r, o, rc.Scheme))
		return mutate()
	})
	rc.logChange(op, before, o)
	return err
}

func (rc *requestContext) deployment() error {
	d := &rc.Deployment
	return rc.createOrUpdate(d, func() error {
		if !d.ObjectMeta.CreationTimestamp.IsZero() && // Existing deployment
			!equality.Semantic.DeepEqual(d.Spec.Selector, &rc.selector) { // Wrong selector
			if err := rc.Delete(rc.ctx, d); err != nil {
				return err
			}
			return fmt.Errorf("Deployment has wrong selector: %v", d.Spec.Selector.MatchLabels)
		}
		d.Spec.Selector = &rc.selector
		d.Spec.Template = corev1.PodTemplateSpec{
			ObjectMeta: metav1.ObjectMeta{Labels: rc.labels},
			Spec: corev1.PodSpec{
				Containers: []corev1.Container{
					{
						Name:  ApplicationName,
						Image: rc.image,
						Command: []string{
							"korrel8r",
							"web",
							"--https=:8443",
							"--cert=/secrets/tls.crt",
							"--key=/secrets/tls.key",
						},
						VolumeMounts: []corev1.VolumeMount{
							{
								Name:      "secrets",
								ReadOnly:  true,
								MountPath: "/secrets",
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
				ServiceAccountName: rc.Korrel8r.Spec.ServiceAccountName,
				Volumes: []corev1.Volume{
					{
						Name:         "secrets",
						VolumeSource: corev1.VolumeSource{Secret: &corev1.SecretVolumeSource{SecretName: rc.Name}},
					},
				},
			},
		}
		// Make additional changes to the container based on Korrel8r.Spec.
		c := &d.Spec.Template.Spec.Containers[0]
		if rc.Korrel8r.Spec.ConfigMap != nil && rc.Korrel8r.Spec.ConfigMap.Name != "" {
			// Use the supplied config map for configuration
			v := &d.Spec.Template.Spec.Volumes
			*v = append(*v, corev1.Volume{ // Add the cm volume
				Name: "config",
				VolumeSource: corev1.VolumeSource{
					ConfigMap: &corev1.ConfigMapVolumeSource{
						LocalObjectReference: corev1.LocalObjectReference{Name: rc.Korrel8r.Spec.ConfigMap.Name},
					}},
			})
			c.VolumeMounts = append(c.VolumeMounts, corev1.VolumeMount{ // Mount the cm volume
				Name:      "config",
				ReadOnly:  true,
				MountPath: "/config",
			})
			c.Command = append(c.Command, "--config=/config/korrel8r.yaml") // Use mounted config.
		} else {
			c.Command = append(c.Command, "--config=/etc/korrel8r/openshift-svc.yaml") // Use config from image.
		}
		if rc.Korrel8r.Spec.Debug != nil {
			c.Env = setEnv(c.Env, VerboseEnv, strconv.Itoa(rc.Korrel8r.Spec.Debug.Verbose))
		}
		return nil
	})
}

func setEnv(env []corev1.EnvVar, name, value string) []corev1.EnvVar {
	i := slices.IndexFunc(env, func(ev corev1.EnvVar) bool { return ev.Name == name })
	if i < 0 {
		return append(env, corev1.EnvVar{Name: name, Value: value})
	}
	env[i].Value = value
	return env
}

func (r *requestContext) service() error {
	s := &r.Service
	return r.createOrUpdate(s, func() error {
		port := intstr.FromInt(8443)
		s.ObjectMeta.Annotations = map[string]string{
			// Generate a TLS server certificate in a secret
			"service.beta.openshift.io/serving-cert-secret-name": r.Name,
		}
		s.Spec = corev1.ServiceSpec{
			Selector: r.selector.MatchLabels,
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

func (rc *requestContext) route() error {
	route := &rc.Route
	err := rc.createOrUpdate(route, func() error {
		route.Spec.TLS = &routev1.TLSConfig{Termination: routev1.TLSTerminationReencrypt}
		route.Spec.To = routev1.RouteTargetReference{
			Kind: "Service",
			Name: rc.Name,
		}
		return nil
	})
	if meta.IsNoMatchError(err) { // Route is optional, no error if unavailable.
		gvk, _ := rc.Client.GroupVersionKindFor(route)
		rc.log.Info("Skip unsupported kind", "kind", gvk.GroupKind()) // Not an error
		return nil
	}
	return err
}

func (r *Korrel8rReconciler) kindOf(o client.Object) string {
	gvk, _ := r.Client.GroupVersionKindFor(o)
	return gvk.Kind
}
