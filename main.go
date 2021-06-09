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

	if !isManager {
		setupLog.Info("Starting as a poison pill agent that should run as part of the daemonset")

		myNodeName := os.Getenv(nodeNameEnvVar)
		if myNodeName == "" {
			setupLog.Error(err, "failed to get own node name", "env var name", nodeNameEnvVar)
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
		peerUpdateInterval := 15 * time.Second
		myPeers := peers.New(myNodeName, peerUpdateInterval, mgr.GetClient(), ctrl.Log.WithName("peers"))
		if err = mgr.Add(myPeers); err != nil {
			setupLog.Error(err, "failed to add peers to the manager")
			os.Exit(1)
		}

		// TODO make the interval and error threshold configurable?
		apiCheckInterval := 15 * time.Second
		maxErrorThreshold := 3
		// use API reader for not using the cache, which would prevent detecting API errors
		apiCheck := apicheck.New(myNodeName, myPeers, rebooter, apiCheckInterval, maxErrorThreshold, mgr.GetConfig(), ctrl.Log.WithName("api-check"))
		if err = mgr.Add(apiCheck); err != nil {
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
		minTimeToAssumeNodeRebooted := time.Duration(maxErrorThreshold) * apiCheckInterval
		// then add the watchdog timeout
		if watchdog != nil {
			minTimeToAssumeNodeRebooted += watchdog.GetTimeout()
		}
		// and add some buffer
		minTimeToAssumeNodeRebooted += 5 * time.Second

		if timeToAssumeNodeRebooted < minTimeToAssumeNodeRebooted {
			timeToAssumeNodeRebooted = minTimeToAssumeNodeRebooted
		}

		if err = (&controllers.PoisonPillRemediationReconciler{
			Client:                       mgr.GetClient(),
			Log:                          ctrl.Log.WithName("controllers").WithName("PoisonPillRemediation"),
			Scheme:                       mgr.GetScheme(),
			Rebooter:                     rebooter,
			SafeTimeToAssumeNodeRebooted: timeToAssumeNodeRebooted,
			MyNodeName:                   myNodeName,
		}).SetupWithManager(mgr); err != nil {
			setupLog.Error(err, "unable to create controller", "controller", "PoisonPillRemediation")
			os.Exit(1)
		}

		// TODO make this also a Runnable and let the manager start it
		setupLog.Info("starting web server")
		go pa.Start()

	} else {
		setupLog.Info("Starting as a manager that installs the daemonset")
		if err = (&controllers.PoisonPillConfigReconciler{
			Client:            mgr.GetClient(),
			Log:               ctrl.Log.WithName("controllers").WithName("PoisonPillConfig"),
			Scheme:            mgr.GetScheme(),
			InstallFileFolder: "./install",
		}).SetupWithManager(mgr); err != nil {
			setupLog.Error(err, "unable to create controller", "controller", "PoisonPillConfig")
			os.Exit(1)
		}
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
