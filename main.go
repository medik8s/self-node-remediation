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
	"github.com/medik8s/poison-pill/pkg/utils"
	"os"
	"strconv"
	"time"

	"github.com/pkg/errors"

	// Import all Kubernetes client auth plugins (e.g. Azure, GCP, OIDC, etc.)
	// to ensure that exec-entrypoint and run can make use of them.
	_ "k8s.io/client-go/plugin/pkg/client/auth"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/runtime"
	utilruntime "k8s.io/apimachinery/pkg/util/runtime"
	clientgoscheme "k8s.io/client-go/kubernetes/scheme"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/healthz"
	"sigs.k8s.io/controller-runtime/pkg/log/zap"
	"sigs.k8s.io/controller-runtime/pkg/manager"

	machinev1beta1 "github.com/openshift/machine-api-operator/pkg/apis/machine/v1beta1"

	poisonpillv1alpha1 "github.com/medik8s/poison-pill/api/v1alpha1"
	"github.com/medik8s/poison-pill/controllers"
	"github.com/medik8s/poison-pill/pkg/apicheck"
	"github.com/medik8s/poison-pill/pkg/certificates"
	"github.com/medik8s/poison-pill/pkg/peerhealth"
	"github.com/medik8s/poison-pill/pkg/peers"
	"github.com/medik8s/poison-pill/pkg/reboot"
	"github.com/medik8s/poison-pill/pkg/watchdog"
	//+kubebuilder:scaffold:imports
)

const (
	nodeNameEnvVar        = "MY_NODE_NAME"
	peerHealthDefaultPort = 30001
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
	ns, err := getDeploymentNamespace()
	if err != nil {
		setupLog.Error(err, "failed to get deployed namespace from env var")
		os.Exit(1)
	}

	if err := (&controllers.PoisonPillConfigReconciler{
		Client:            mgr.GetClient(),
		Log:               ctrl.Log.WithName("controllers").WithName("PoisonPillConfig"),
		Scheme:            mgr.GetScheme(),
		InstallFileFolder: "./install",
		DefaultPpcCreator: newConfigIfNotExist,
		Namespace:         ns,
	}).SetupWithManager(mgr); err != nil {
		setupLog.Error(err, "unable to create controller", "controller", "PoisonPillConfig")
		os.Exit(1)
	}

	if err := newConfigIfNotExist(mgr.GetClient()); err != nil {
		setupLog.Error(err, "failed to create a default poison pill config CR")
		os.Exit(1)
	}

	if err := newDefaultTemplateIfNotExist(mgr.GetClient()); err != nil {
		setupLog.Error(err, "failed to create default remediation template")
		os.Exit(1)
	}
}

func getDurEnvVarOrDie(varName string) time.Duration {
	intVar := getIntEnvVarOrDie(varName)
	return time.Duration(intVar) * time.Second
}

func getIntEnvVarOrDie(varName string) int {
	// determine safe reboot time
	varVal := os.Getenv(varName)
	intVar, err := strconv.Atoi(varVal)
	if err != nil {
		setupLog.Error(err, "failed to convert env variable to int", "var name", varName, "var value", varVal)
		os.Exit(1)
	}
	return intVar
}

func initPoisonPillAgent(mgr manager.Manager) {
	setupLog.Info("Starting as a poison pill agent that should run as part of the daemonset")

	myNodeName := os.Getenv(nodeNameEnvVar)
	if myNodeName == "" {
		setupLog.Error(errors.New("failed to get own node name"), "node name was empty",
			"env var name", nodeNameEnvVar)
	}

	ns, err := getDeploymentNamespace()
	if err != nil {
		setupLog.Error(err, "unable to get the deployment namespace")
		os.Exit(1)
	}

	wasWatchdogInitiated := false
	wd, err := watchdog.NewLinux(ctrl.Log.WithName("watchdog"))
	if err != nil {
		setupLog.Error(err, "failed to init watchdog, using soft reboot")
	}

	if wd != nil {
		if err = mgr.Add(wd); err != nil {
			setupLog.Error(err, "failed to add watchdog to the manager")
			os.Exit(1)
		}
		wasWatchdogInitiated = true
	}

	if err = utils.UpdateNodeWithIsRebootCapableAnnotation(wasWatchdogInitiated, myNodeName, mgr); err != nil {
		setupLog.Error(err, "failed to update node's annotation", "annotation", utils.IsRebootCapableAnnotation)
		os.Exit(1)
	}

	// it's fine when the watchdog is nil!
	rebooter := reboot.NewWatchdogRebooter(wd, ctrl.Log.WithName("rebooter"))

	// TODO make the interval configurable
	peerUpdateInterval := getDurEnvVarOrDie("PEER_UPDATE_INTERVAL")
	peerApiServerTimeout := getDurEnvVarOrDie("PEER_API_SERVER_TIMEOUT")

	myPeers := peers.New(myNodeName, peerUpdateInterval, mgr.GetClient(), ctrl.Log.WithName("peers"), peerApiServerTimeout)
	if err = mgr.Add(myPeers); err != nil {
		setupLog.Error(err, "failed to add peers to the manager")
		os.Exit(1)
	}

	// TODO make the interval and error threshold configurable?
	apiCheckInterval := getDurEnvVarOrDie("API_CHECK_INTERVAL")       //the frequency for api-server connectivity check
	maxErrorThreshold := getIntEnvVarOrDie("MAX_API_ERROR_THRESHOLD") //after this threshold, the node will start contacting its peers
	apiServerTimeout := getDurEnvVarOrDie("API_SERVER_TIMEOUT")       //timeout for each api-connectivity check
	peerDialTimeout := getDurEnvVarOrDie("PEER_DIAL_TIMEOUT")         //timeout for establishing connection to peer
	peerRequestTimeout := getDurEnvVarOrDie("PEER_REQUEST_TIMEOUT")   //timeout for each peer request

	// init certificate reader
	certReader := certificates.NewSecretCertStorage(mgr.GetClient(), ctrl.Log.WithName("SecretCertStorage"), ns)

	apiConnectivityCheckConfig := &apicheck.ApiConnectivityCheckConfig{
		Log:                ctrl.Log.WithName("api-check"),
		MyNodeName:         myNodeName,
		CheckInterval:      apiCheckInterval,
		MaxErrorsThreshold: maxErrorThreshold,
		Peers:              myPeers,
		Rebooter:           rebooter,
		Cfg:                mgr.GetConfig(),
		CertReader:         certReader,
		ApiServerTimeout:   apiServerTimeout,
		PeerDialTimeout:    peerDialTimeout,
		PeerRequestTimeout: peerRequestTimeout,
		PeerHealthPort:     peerHealthDefaultPort,
	}

	apiChecker := apicheck.New(apiConnectivityCheckConfig)
	if err = mgr.Add(apiChecker); err != nil {
		setupLog.Error(err, "failed to add api-check to the manager")
		os.Exit(1)
	}

	// determine safe reboot time
	timeToAssumeNodeRebooted := getDurEnvVarOrDie("TIME_TO_ASSUME_NODE_REBOOTED")

	// but the reboot time needs be at least the time we know we need for determining a node issue and trigger the reboot!
	// 1. time for determing node issue
	minTimeToAssumeNodeRebooted := (apiCheckInterval + apiServerTimeout) * time.Duration(maxErrorThreshold)
	// 2. time for asking peers (10% batches + 1st smaller batch)
	minTimeToAssumeNodeRebooted += (10 + 1) * (peerDialTimeout + peerRequestTimeout)
	// 3. watchdog timeout
	if wd != nil {
		minTimeToAssumeNodeRebooted += wd.GetTimeout()
	}
	// 4. some buffer
	minTimeToAssumeNodeRebooted += 15 * time.Second

	if timeToAssumeNodeRebooted < minTimeToAssumeNodeRebooted {
		timeToAssumeNodeRebooted = minTimeToAssumeNodeRebooted
	}
	setupLog.Info("Time to assume that unhealthy node has been rebooted", "time", timeToAssumeNodeRebooted)

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

	setupLog.Info("init grpc server")
	// TODO make port configurable?
	server, err := peerhealth.NewServer(pprReconciler, mgr.GetConfig(), ctrl.Log.WithName("peerhealth").WithName("server"), peerHealthDefaultPort, certReader)
	if err != nil {
		setupLog.Error(err, "failed to init grpc server")
		os.Exit(1)
	}
	if err = mgr.Add(server); err != nil {
		setupLog.Error(err, "failed to add grpc server to the manager")
		os.Exit(1)
	}
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

// newDefaultTemplateIfNotExist creates a new PoisonPillRemediationTemplate object
func newDefaultTemplateIfNotExist(c client.Client) error {
	ns, err := getDeploymentNamespace()
	if err != nil {
		return errors.Wrap(err, "unable to get the deployment namespace")
	}

	pprt := poisonpillv1alpha1.NewDefaultRemediationTemplate()
	pprt.SetNamespace(ns)

	err = c.Create(context.Background(), &pprt, &client.CreateOptions{})
	if err != nil && !apierrors.IsAlreadyExists(err) {
		return errors.Wrap(err, "failed to create a default poison pill template CR")
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
