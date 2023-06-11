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
	"flag"
	"os"
	"strconv"
	"time"

	"github.com/pkg/errors"
	"go.uber.org/zap/zapcore"

	"k8s.io/apimachinery/pkg/runtime"
	utilruntime "k8s.io/apimachinery/pkg/util/runtime"
	clientgoscheme "k8s.io/client-go/kubernetes/scheme"

	// Import all Kubernetes client auth plugins (e.g. Azure, GCP, OIDC, etc.)
	// to ensure that exec-entrypoint and run can make use of them.
	_ "k8s.io/client-go/plugin/pkg/client/auth"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/healthz"
	"sigs.k8s.io/controller-runtime/pkg/log/zap"
	"sigs.k8s.io/controller-runtime/pkg/manager"

	machinev1beta1 "github.com/openshift/machine-api-operator/pkg/apis/machine/v1beta1"

	selfnoderemediationv1alpha1 "github.com/medik8s/self-node-remediation/api/v1alpha1"
	"github.com/medik8s/self-node-remediation/controllers"
	"github.com/medik8s/self-node-remediation/pkg/apicheck"
	"github.com/medik8s/self-node-remediation/pkg/certificates"
	"github.com/medik8s/self-node-remediation/pkg/controlplane"
	"github.com/medik8s/self-node-remediation/pkg/peerhealth"
	"github.com/medik8s/self-node-remediation/pkg/peers"
	"github.com/medik8s/self-node-remediation/pkg/reboot"
	"github.com/medik8s/self-node-remediation/pkg/snrconfighelper"
	"github.com/medik8s/self-node-remediation/pkg/template"
	"github.com/medik8s/self-node-remediation/pkg/utils"
	"github.com/medik8s/self-node-remediation/pkg/watchdog"
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

	utilruntime.Must(selfnoderemediationv1alpha1.AddToScheme(scheme))
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
		"Used to differentiate between the self node remediation agents that runs in a daemonset to the 'manager' that only"+
			"reconciles the config CRD and installs the DS")
	opts := zap.Options{
		Development: true,
		TimeEncoder: zapcore.RFC3339NanoTimeEncoder,
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
		initSelfNodeRemediationManager(mgr)
	} else {
		initSelfNodeRemediationAgent(mgr)
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

func initSelfNodeRemediationManager(mgr manager.Manager) {
	setupLog.Info("Starting as a manager that installs the daemonset")

	if err := utils.InitOutOfServiceTaintSupportedFlag(mgr.GetConfig()); err != nil {
		utils.IsOutOfServiceTaintSupported = false
		setupLog.Error(err, "unable to verify out of service taint support. out of service taint isn't supported")
	}

	if err := (&selfnoderemediationv1alpha1.SelfNodeRemediationConfig{}).SetupWebhookWithManager(mgr); err != nil {
		setupLog.Error(err, "unable to create webhook", "webhook", "SelfNodeRemediationConfig")
		os.Exit(1)
	}

	if err := (&selfnoderemediationv1alpha1.SelfNodeRemediationTemplate{}).SetupWebhookWithManager(mgr); err != nil {
		setupLog.Error(err, "unable to create webhook", "webhook", "SelfNodeRemediationTemplate")
		os.Exit(1)
	}

	if err := (&selfnoderemediationv1alpha1.SelfNodeRemediation{}).SetupWebhookWithManager(mgr); err != nil {
		setupLog.Error(err, "unable to create webhook", "webhook", "SelfNodeRemediation")
		os.Exit(1)
	}

	ns, err := utils.GetDeploymentNamespace()
	if err != nil {
		setupLog.Error(err, "failed to get deployed namespace from env var")
		os.Exit(1)
	}

	if err := (&controllers.SelfNodeRemediationConfigReconciler{
		Client:            mgr.GetClient(),
		Log:               ctrl.Log.WithName("controllers").WithName("SelfNodeRemediationConfig"),
		Scheme:            mgr.GetScheme(),
		InstallFileFolder: "./install",
		DefaultPpcCreator: snrconfighelper.NewConfigIfNotExist,
		Namespace:         ns,
	}).SetupWithManager(mgr); err != nil {
		setupLog.Error(err, "unable to create controller", "controller", "SelfNodeRemediationConfig")
		os.Exit(1)
	}

	snrConfigInit := snrconfighelper.New(mgr.GetClient(), ctrl.Log.WithName("default SelfNodeRemediationConfig"))
	if err = mgr.Add(snrConfigInit); err != nil {
		setupLog.Error(err, "failed to add config to the manager")
		os.Exit(1)
	}

	templateCreator := template.New(mgr.GetClient(), ctrl.Log.WithName("template creator"))
	if err = mgr.Add(templateCreator); err != nil {
		setupLog.Error(err, "failed to add template creator to the manager")
		os.Exit(1)
	}
}

func getDurEnvVarOrDie(varName string) time.Duration {
	intVar := getIntEnvVarOrDie(varName)
	return time.Duration(intVar)
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

func initSelfNodeRemediationAgent(mgr manager.Manager) {
	setupLog.Info("Starting as a self node remediation agent that should run as part of the daemonset")

	myNodeName := os.Getenv(nodeNameEnvVar)
	if myNodeName == "" {
		setupLog.Error(errors.New("failed to get own node name"), "node name was empty",
			"env var name", nodeNameEnvVar)
	}

	ns, err := utils.GetDeploymentNamespace()
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
	timeToAssumeNodeRebooted := getDurEnvVarOrDie("TIME_TO_ASSUME_NODE_REBOOTED")

	safeRebootCalc := reboot.NewSafeTimeCalculator(mgr.GetClient(), wd, maxErrorThreshold, apiCheckInterval, apiServerTimeout, peerDialTimeout, peerRequestTimeout, timeToAssumeNodeRebooted)
	if err = mgr.Add(safeRebootCalc); err != nil {
		setupLog.Error(err, "failed to add safe reboot time calculator to the manager")
		os.Exit(1)
	}

	// it's fine when the watchdog is nil!
	rebooter := reboot.NewWatchdogRebooter(wd, ctrl.Log.WithName("rebooter"), safeRebootCalc)

	// init certificate reader
	certReader := certificates.NewSecretCertStorage(mgr.GetClient(), ctrl.Log.WithName("SecretCertStorage"), ns)

	apiConnectivityCheckConfig := &apicheck.ApiConnectivityCheckConfig{
		Log:                       ctrl.Log.WithName("api-check"),
		MyNodeName:                myNodeName,
		CheckInterval:             apiCheckInterval,
		MaxErrorsThreshold:        maxErrorThreshold,
		Peers:                     myPeers,
		Rebooter:                  rebooter,
		Cfg:                       mgr.GetConfig(),
		CertReader:                certReader,
		ApiServerTimeout:          apiServerTimeout,
		PeerDialTimeout:           peerDialTimeout,
		PeerRequestTimeout:        peerRequestTimeout,
		PeerHealthPort:            peerHealthDefaultPort,
		MaxTimeForNoPeersResponse: reboot.MaxTimeForNoPeersResponse,
	}

	controlPlaneManager := controlplane.NewManager(myNodeName, mgr.GetClient())

	if err = mgr.Add(controlPlaneManager); err != nil {
		setupLog.Error(err, "failed to add controlPlane remediation manager to setup manager")
		os.Exit(1)
	}

	apiChecker := apicheck.New(apiConnectivityCheckConfig, controlPlaneManager)
	if err = mgr.Add(apiChecker); err != nil {
		setupLog.Error(err, "failed to add api-check to the manager")
		os.Exit(1)
	}

	restoreNodeAfter := 90 * time.Second
	snrReconciler := &controllers.SelfNodeRemediationReconciler{
		Client:           mgr.GetClient(),
		Log:              ctrl.Log.WithName("controllers").WithName("SelfNodeRemediation"),
		Scheme:           mgr.GetScheme(),
		Recorder:         mgr.GetEventRecorderFor("SelfNodeRemediation"),
		Rebooter:         rebooter,
		MyNodeName:       myNodeName,
		RestoreNodeAfter: restoreNodeAfter,
	}

	if err = snrReconciler.SetupWithManager(mgr); err != nil {
		setupLog.Error(err, "unable to create controller", "controller", "SelfNodeRemediation")
		os.Exit(1)
	}

	setupLog.Info("init grpc server")
	// TODO make port configurable?
	server, err := peerhealth.NewServer(snrReconciler, mgr.GetConfig(), ctrl.Log.WithName("peerhealth").WithName("server"), peerHealthDefaultPort, certReader)
	if err != nil {
		setupLog.Error(err, "failed to init grpc server")
		os.Exit(1)
	}
	if err = mgr.Add(server); err != nil {
		setupLog.Error(err, "failed to add grpc server to the manager")
		os.Exit(1)
	}
}
