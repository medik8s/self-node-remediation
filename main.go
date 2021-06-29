/*
Copyright 2021.

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

package main

import (
	"context"
	"flag"
	"fmt"
	"os"
	"strconv"
	"time"

	"github.com/pkg/errors"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/manager"

	// Import all Kubernetes client auth plugins (e.g. Azure, GCP, OIDC, etc.)
	// to ensure that exec-entrypoint and run can make use of them.
	_ "k8s.io/client-go/plugin/pkg/client/auth"

	"k8s.io/apimachinery/pkg/runtime"
	utilruntime "k8s.io/apimachinery/pkg/util/runtime"
	clientgoscheme "k8s.io/client-go/kubernetes/scheme"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/healthz"
	"sigs.k8s.io/controller-runtime/pkg/log/zap"

	machinev1beta1 "github.com/openshift/machine-api-operator/pkg/apis/machine/v1beta1"

	poisonpillv1alpha1 "github.com/medik8s/poison-pill/api/v1alpha1"
	"github.com/medik8s/poison-pill/controllers"
	"github.com/medik8s/poison-pill/pkg/apicheck"
	pa "github.com/medik8s/poison-pill/pkg/peerassistant"
	"github.com/medik8s/poison-pill/pkg/peers"
	"github.com/medik8s/poison-pill/pkg/reboot"
	"github.com/medik8s/poison-pill/pkg/watchdog"
	//+kubebuilder:scaffold:imports
)

const (
	nodeNameEnvVar = "MY_NODE_NAME"
)

var (
	scheme   = runtime.NewScheme()
	setupLog = ctrl.Log.WithName("setup")
)

func init() {
	utilruntime.Must(clientgoscheme.AddToScheme(scheme))

	utilruntime.Must(poisonpillv1alpha1.AddToScheme(scheme))
	utilruntime.Must(machinev1beta1.AddToScheme(scheme))
	//+kubebuilder:scaffold:scheme
}

func main() {
	var metricsAddr string
	var enableLeaderElection bool
	var probeAddr string
	var isManager bool
	flag.StringVar(&metricsAddr, "metrics-bind-address", ":8080", "The address the metric endpoint binds to.")
	flag.StringVar(&probeAddr, "health-probe-bind-address", ":8081", "The address the probe endpoint binds to.")
	flag.BoolVar(&enableLeaderElection, "leader-elect", false,
		"Enable leader election for controller manager. "+
			"Enabling this will ensure there is only one active controller manager.")
	flag.BoolVar(&isManager, "is-manager", false,
		"Used to differentiate between the poison pill agents that runs in a daemonset to the 'manager' that only"+
			"reconciles the config CRD and installs the DS")
	opts := zap.Options{
		Development: true,
	}
	opts.BindFlags(flag.CommandLine)
	flag.Parse()

	ctrl.SetLogger(zap.New(zap.UseFlagOptions(&opts)))

	mgr, err := ctrl.NewManager(ctrl.GetConfigOrDie(), ctrl.Options{
		Scheme:                 scheme,
		MetricsBindAddress:     metricsAddr,
		Port:                   9443,
		HealthProbeBindAddress: probeAddr,
		LeaderElection:         enableLeaderElection,
		LeaderElectionID:       "547f6cb6.medik8s.io",
	})
	if err != nil {
		setupLog.Error(err, "unable to start manager")
		os.Exit(1)
	}

	if isManager {
		initPoisonPillManager(mgr)
	} else {
		initPoisonPillAgent(mgr)
	}

	//+kubebuilder:scaffold:builder

	if err := mgr.AddHealthzCheck("healthz", healthz.Ping); err != nil {
		setupLog.Error(err, "unable to set up health check")
		os.Exit(1)
	}
	if err := mgr.AddReadyzCheck("readyz", healthz.Ping); err != nil {
		setupLog.Error(err, "unable to set up ready check")
		os.Exit(1)
	}

	setupLog.Info("starting manager")
	if err := mgr.Start(ctrl.SetupSignalHandler()); err != nil {
		setupLog.Error(err, "problem running manager")
		os.Exit(1)
	}
}

func initPoisonPillManager(mgr manager.Manager) {
	setupLog.Info("Starting as a manager that installs the daemonset")
	if err := (&controllers.PoisonPillConfigReconciler{
		Client:            mgr.GetClient(),
		Log:               ctrl.Log.WithName("controllers").WithName("PoisonPillConfig"),
		Scheme:            mgr.GetScheme(),
		InstallFileFolder: "./install",
	}).SetupWithManager(mgr); err != nil {
		setupLog.Error(err, "unable to create controller", "controller", "PoisonPillConfig")
		os.Exit(1)
	}

	if err := newConfigIfNotExist(mgr.GetClient()); err != nil {
		setupLog.Error(err, "failed to create a default poison pill config CR")
		os.Exit(1)
	}
}

func initPoisonPillAgent(mgr manager.Manager) {
	setupLog.Info("Starting as a poison pill agent that should run as part of the daemonset")

	myNodeName := os.Getenv(nodeNameEnvVar)
	if myNodeName == "" {
		setupLog.Error(errors.New("failed to get own node name"), "node name was empty",
			"env var name", nodeNameEnvVar)
	}

	watchdog, err := watchdog.NewLinux(ctrl.Log.WithName("watchdog"))
	if err != nil {
		setupLog.Error(err, "failed to init watchdog, using soft reboot")
	}
	if watchdog != nil {
		if err = mgr.Add(watchdog); err != nil {
			setupLog.Error(err, "failed to add watchdog to the manager")
			os.Exit(1)
		}
	}
	// it's fine when the watchdog is nil!
	rebooter := reboot.NewWatchdogRebooter(watchdog, ctrl.Log.WithName("rebooter"))

	// TODO make the interval configurable
	peerUpdateInterval := 15 * time.Minute
	peerApiServerTimeout := 5 * time.Second

	myPeers := peers.New(myNodeName, peerUpdateInterval, mgr.GetClient(), ctrl.Log.WithName("peers"), peerApiServerTimeout)
	if err = mgr.Add(myPeers); err != nil {
		setupLog.Error(err, "failed to add peers to the manager")
		os.Exit(1)
	}

	// TODO make the interval and error threshold configurable?
	apiCheckInterval := 15 * time.Second //the frequency for api-server connectivity check
	maxErrorThreshold := 3               //after this threshold, the node will start contacting its peers
	apiServerTimeout := 5 * time.Second  //timeout for each api-connectivity check
	peerTimeout := 5 * time.Second       //timeout for each peer request

	apiConnectivityCheckConfig := &apicheck.ApiConnectivityCheckConfig{
		Log:                ctrl.Log.WithName("api-check"),
		MyNodeName:         myNodeName,
		CheckInterval:      apiCheckInterval,
		MaxErrorsThreshold: maxErrorThreshold,
		Peers:              myPeers,
		Rebooter:           rebooter,
		Cfg:                mgr.GetConfig(),
		ApiServerTimeout:   apiServerTimeout,
		PeerTimeout:        peerTimeout,
	}

	apiChecker := apicheck.New(apiConnectivityCheckConfig)
	if err = mgr.Add(apiChecker); err != nil {
		setupLog.Error(err, "failed to add api-check to the manager")
		os.Exit(1)
	}

	// determine safe reboot time
	timeToAssumeNodeRebootedInt, err := strconv.Atoi(os.Getenv("TIME_TO_ASSUME_NODE_REBOOTED"))
	if err != nil {
		setupLog.Error(err, "failed to convert env variable TIME_TO_ASSUME_NODE_REBOOTED to int")
		os.Exit(1)
	}
	timeToAssumeNodeRebooted := time.Duration(timeToAssumeNodeRebootedInt) * time.Second

	// but the reboot time needs be at least the time we know we need for determining a node issue and trigger the reboot!
	minTimeToAssumeNodeRebooted := (apiCheckInterval+apiServerTimeout)*time.Duration(maxErrorThreshold) +
		peerApiServerTimeout + (10+1)*peerTimeout
	// then add the watchdog timeout
	if watchdog != nil {
		minTimeToAssumeNodeRebooted += watchdog.GetTimeout()
	}
	// and add some buffer
	minTimeToAssumeNodeRebooted += 15 * time.Second

	if timeToAssumeNodeRebooted < minTimeToAssumeNodeRebooted {
		timeToAssumeNodeRebooted = minTimeToAssumeNodeRebooted
	}
	setupLog.Info("Time to assume that unhealthy node has been rebooted: %v", timeToAssumeNodeRebooted)

	pprReconciler := &controllers.PoisonPillRemediationReconciler{
		Client:                       mgr.GetClient(),
		Log:                          ctrl.Log.WithName("controllers").WithName("PoisonPillRemediation"),
		Scheme:                       mgr.GetScheme(),
		Rebooter:                     rebooter,
		SafeTimeToAssumeNodeRebooted: timeToAssumeNodeRebooted,
		MyNodeName:                   myNodeName,
	}

	if err = pprReconciler.SetupWithManager(mgr); err != nil {
		setupLog.Error(err, "unable to create controller", "controller", "PoisonPillRemediation")
		os.Exit(1)
	}

	// TODO make this also a Runnable and let the manager start it
	setupLog.Info("starting web server")
	go pa.Start(pprReconciler)
}

// newConfigIfNotExist creates a new PoisonPillConfig object
// to initialize the rest of the deployment objects creation.
func newConfigIfNotExist(c client.Client) error {
	ns, err := getDeploymentNamespace()
	if err != nil {
		return errors.Wrap(err, "unable to get the deployment namespace")
	}

	config := poisonpillv1alpha1.NewDefaultPoisonPillConfig()
	config.SetNamespace(ns)

	err = c.Create(context.Background(), &config, &client.CreateOptions{})
	if err != nil && !apierrors.IsAlreadyExists(err) {
		return errors.Wrap(err, "failed to create a default poison pill config CR")
	}
	return nil
}

// getDeploymentNamespace returns the Namespace this operator is deployed on.
func getDeploymentNamespace() (string, error) {
	// deployNamespaceEnvVar is the constant for env variable DEPLOYMENT_NAMESPACE
	// which specifies the Namespace to watch.
	// An empty value means the operator is running with cluster scope.
	var deployNamespaceEnvVar = "DEPLOYMENT_NAMESPACE"

	ns, found := os.LookupEnv(deployNamespaceEnvVar)
	if !found {
		return "", fmt.Errorf("%s must be set", deployNamespaceEnvVar)
	}
	return ns, nil
}
