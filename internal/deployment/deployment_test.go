package deployment

import (
	"io"
	"os"
	"testing"

	. "github.com/onsi/gomega"

	appsv1 "k8s.io/api/apps/v1"
	"k8s.io/apimachinery/pkg/util/intstr"
	"k8s.io/apimachinery/pkg/util/yaml"
)

const managerYAMLPath = "../../config/manager/manager.yaml"

func parseManagerDeployment(t *testing.T) *appsv1.Deployment {
	t.Helper()
	f, err := os.Open(managerYAMLPath)
	if err != nil {
		t.Fatalf("failed to open manager.yaml: %v", err)
	}
	defer f.Close()

	decoder := yaml.NewYAMLOrJSONDecoder(f, 4096)
	for {
		obj := &appsv1.Deployment{}
		if err := decoder.Decode(obj); err != nil {
			if err == io.EOF {
				break
			}
			t.Fatalf("failed to decode document from manager.yaml: %v", err)
		}
		if obj.Kind == "Deployment" {
			return obj
		}
	}
	t.Fatal("no Deployment found in manager.yaml")
	return nil
}

// TestManagerRolloutStrategyPreventsUpgradeDeadlock validates that the manager deployment
// uses maxSurge=0 and maxUnavailable=1.
//
// With topologySpreadConstraints (DoNotSchedule) and 2 replicas, the default maxSurge
// causes a 3rd pod to be created during rolling updates. On 2-worker clusters, this pod
// cannot satisfy the spread constraint and remains Pending forever, blocking OLM upgrades.
func TestManagerRolloutStrategyPreventsUpgradeDeadlock(t *testing.T) {
	g := NewGomegaWithT(t)
	dep := parseManagerDeployment(t)

	g.Expect(dep.Spec.Strategy.Type).To(Equal(appsv1.RollingUpdateDeploymentStrategyType))
	g.Expect(dep.Spec.Strategy.RollingUpdate).NotTo(BeNil())
	g.Expect(*dep.Spec.Strategy.RollingUpdate.MaxSurge).To(Equal(intstr.FromInt(0)),
		"maxSurge must be 0 to prevent upgrade deadlock on 2-worker clusters")
	g.Expect(*dep.Spec.Strategy.RollingUpdate.MaxUnavailable).To(Equal(intstr.FromInt(1)),
		"maxUnavailable must be 1 to allow progress by terminating old pods first")
}
