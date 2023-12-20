// Copyright: This file is part of korrel8r, released under https://github.com/korrel8r/korrel8r/blob/main/LICENSE

package main

import (
	"flag"
	"os"
	"strconv"

	// Import all Kubernetes client auth plugins (e.g. Azure, GCP, OIDC, etc.)
	// to ensure that exec-entrypoint and run can make use of them.
	_ "k8s.io/client-go/plugin/pkg/client/auth"

	"github.com/go-logr/stdr"
	"github.com/korrel8r/operator/controllers"
	"k8s.io/apimachinery/pkg/runtime"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/healthz"
	"sigs.k8s.io/controller-runtime/pkg/manager"
	"sigs.k8s.io/controller-runtime/pkg/metrics/server"
	//+kubebuilder:scaffold:imports
)

const verboseEnv = "KORREL8R_VERBOSE"

func main() {
	metricsAddr := flag.String("metrics-bind-address", ":9090", "The address the metric endpoint binds to.")
	probeAddr := flag.String("health-probe-bind-address", ":9091", "The address the probe endpoint binds to.")
	verbose := flag.Int("verbose", 0, "Logging verbosity")
	flag.Parse()
	if env := os.Getenv(verboseEnv); env != "" && *verbose == 0 {
		*verbose, _ = strconv.Atoi(env)
	}

	stdr.SetVerbosity(*verbose)
	log := stdr.New(nil).WithName(controllers.ApplicationName)
	ctrl.SetLogger(log)

	check := func(err error, msg string) {
		if err != nil {
			log.Error(err, msg)
			os.Exit(1)
		}
	}

	scheme := runtime.NewScheme()
	check(controllers.AddToScheme(scheme), "Cannot add controller types to scheme")
	mgr, err := manager.New(ctrl.GetConfigOrDie(), ctrl.Options{
		Scheme:                 scheme,
		Metrics:                server.Options{BindAddress: *metricsAddr},
		HealthProbeBindAddress: *probeAddr,
	})
	check(err, "Unable to start manager")

	kr := controllers.NewKorrel8rReconciler(
		mgr.GetClient(),
		mgr.GetScheme(),
		mgr.GetEventRecorderFor(controllers.ApplicationName))
	check(kr.SetupWithManager(mgr), "Unable to create controller")
	check(mgr.AddHealthzCheck("healthz", healthz.Ping), "Unable to set up health check")
	check(mgr.AddReadyzCheck("readyz", healthz.Ping), "Unable to set up ready check")

	log.Info("Starting manager")
	check(mgr.Start(ctrl.SetupSignalHandler()), "Problem running manager")
}
