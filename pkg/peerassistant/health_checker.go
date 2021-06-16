package peerassistant

import (
	"context"
	poisonPillApis "github.com/medik8s/poison-pill/api"
	"github.com/medik8s/poison-pill/api/v1alpha1"
	"github.com/medik8s/poison-pill/controllers"
	corev1 "k8s.io/api/core/v1"
	apiErrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/client-go/dynamic"
	"k8s.io/client-go/rest"
	"k8s.io/client-go/tools/cache"
	"sigs.k8s.io/controller-runtime/pkg/log/zap"
)

const machineAnnotation = "machine.openshift.io/machine" //todo this is openshift specific

var (
	client dynamic.Interface
	pprRes = schema.GroupVersionResource{Group: v1alpha1.GroupVersion.Group,
		Version:  v1alpha1.GroupVersion.Version,
		Resource: "poisonpillremediations"}
	nodeRes = schema.GroupVersionResource{Group: corev1.SchemeGroupVersion.Group,
		Version:  corev1.SchemeGroupVersion.Version,
		Resource: "nodes"}
	logger = zap.New().WithName("health-checker")
)

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

func isHealthy(nodeName string) poisonPillApis.HealthCheckResponse {
	logger.Info("checking health for", "node", nodeName)

	namespace := controllers.GetLastSeenPprNamespace()
	isMachine := controllers.IsLastSeenPprWasMachine()

	if isMachine {
		return isHealthyMachine(nodeName, namespace)
	} else {
		return isHealthyNode(nodeName, namespace)
	}
}

func isHealthyNode(nodeName string, namespace string) poisonPillApis.HealthCheckResponse {
	return isHealthyByPpr(nodeName, namespace)
}

func isHealthyByPpr(pprName string, pprNamespace string) poisonPillApis.HealthCheckResponse {
	_, err := client.Resource(pprRes).Namespace(pprNamespace).Get(context.TODO(), pprName, metav1.GetOptions{})
	if err != nil {
		if apiErrors.IsNotFound(err) {
			logger.Info("healthy")
			return poisonPillApis.Healthy
		}
		logger.Error(err, "api error")
		return poisonPillApis.ApiError
	}

	logger.Info("unhealthy")
	return poisonPillApis.Unhealthy
}

func isHealthyMachine(nodeName string, namespace string) poisonPillApis.HealthCheckResponse {
	node, err := client.Resource(nodeRes).Namespace("").Get(context.TODO(), nodeName, metav1.GetOptions{})
	if err != nil {
		logger.Error(err, "api error")
		return poisonPillApis.ApiError
	}

	ann := node.GetAnnotations()
	namespacedMachine, exists := ann[machineAnnotation]

	if !exists {
		logger.Info("node doesn't have machine annotation")
		return poisonPillApis.Unhealthy //todo is this the correct response?
	}
	_, machineName, err := cache.SplitMetaNamespaceKey(namespacedMachine)

	if err != nil {
		logger.Error(err, "failed to parse machine annotation on the node")
		return poisonPillApis.Unhealthy //todo is this the correct response?
	}

	return isHealthyByPpr(machineName, namespace)
}
