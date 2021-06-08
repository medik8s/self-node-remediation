package peerassistant

import (
	"context"
	poisonPillApis "github.com/medik8s/poison-pill/api"
	"github.com/medik8s/poison-pill/api/v1alpha1"
	apiErrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/client-go/dynamic"
	"k8s.io/client-go/rest"
	"sigs.k8s.io/controller-runtime/pkg/log/zap"
)

const (
	pprNamespace = "medik8s"
)

var client dynamic.Interface
var pprRes = schema.GroupVersionResource{Group: v1alpha1.GroupVersion.Group,
	Version:  v1alpha1.GroupVersion.Version,
	Resource: "poisonpillremediations"}

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
	log := zap.New().WithName("health-checker")
	log.Info("checking health for", "node", nodeName)
	_, err := client.Resource(pprRes).Namespace(pprNamespace).Get(context.TODO(), nodeName, metav1.GetOptions{})
	if err != nil {
		if apiErrors.IsNotFound(err) {
			log.Info("healthy")
			return poisonPillApis.Healthy
		}
		log.Info("api errror")
		return poisonPillApis.ApiError
	}

	log.Info("unhealthy")
	return poisonPillApis.Unhealthy
}
