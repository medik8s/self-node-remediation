package main

import (
	"context"
	poisonPillApis "github.com/n1r1/poison-pill/api"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/client-go/dynamic"
	"k8s.io/client-go/rest"
)

const (
	externalRemediationAnnotation = "host.metal3.io/external-remediation"
	machineNamespace              = "openshift-machine-api"
)

var client dynamic.Interface
var machineRes = schema.GroupVersionResource{Group: "machine.openshift.io", Version: "v1beta1", Resource: "machines"}

func init() {
	// creates the in-cluster config
	config, err := rest.InClusterConfig()
	if err != nil {
		panic(err.Error())
	}

	// creates client
	client, err = dynamic.NewForConfig(config)
	if err != nil {
		panic(err.Error())
	}
}

func isHealthy(machineName string) poisonPillApis.HealthCheckResponse {
	machine, err := client.Resource(machineRes).Namespace(machineNamespace).Get(context.TODO(), machineName, metav1.GetOptions{})
	if err != nil {
		//todo examine the error, maybe it's 404 etc.
		return poisonPillApis.ApiError
	}

	if len(machine.GetAnnotations()) <= 0 {
		return poisonPillApis.Healthy
	}

	_, isUnhealthy := machine.GetAnnotations()[externalRemediationAnnotation]

	if isUnhealthy {
		return poisonPillApis.Unhealthy
	}

	return poisonPillApis.Healthy
}
