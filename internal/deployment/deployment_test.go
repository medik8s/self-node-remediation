package deployment

import (
	"io"
	"os"
	"testing"

	. "github.com/onsi/gomega"

	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/util/intstr"
	"k8s.io/apimachinery/pkg/util/yaml"
	"k8s.io/utils/ptr"
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
	g.Expect(dep.Spec.Strategy.RollingUpdate.MaxSurge).NotTo(BeNil(), "maxSurge must be explicitly set")
	g.Expect(*dep.Spec.Strategy.RollingUpdate.MaxSurge).To(Equal(intstr.FromInt32(0)),
		"maxSurge must be 0 to prevent upgrade deadlock on 2-worker clusters")
	g.Expect(dep.Spec.Strategy.RollingUpdate.MaxUnavailable).NotTo(BeNil(), "maxUnavailable must be explicitly set")
	g.Expect(*dep.Spec.Strategy.RollingUpdate.MaxUnavailable).To(Equal(intstr.FromInt32(1)),
		"maxUnavailable must be 1 to allow progress by terminating old pods first")
}

// TestManagerDeploymentHAAndScheduling validates replicas, priorityClass, and
// topologySpreadConstraints that are tightly coupled to the rollout strategy.
func TestManagerDeploymentHAAndScheduling(t *testing.T) {
	g := NewGomegaWithT(t)
	dep := parseManagerDeployment(t)

	g.Expect(dep.Spec.Replicas).NotTo(BeNil())
	g.Expect(*dep.Spec.Replicas).To(Equal(int32(2)),
		"replicas must be 2 for HA")
	g.Expect(dep.Spec.Template.Spec.PriorityClassName).To(Equal("system-cluster-critical"),
		"priorityClassName must be system-cluster-critical to prevent eviction under node pressure")

	g.Expect(dep.Spec.Template.Spec.TopologySpreadConstraints).To(HaveLen(1))
	tsc := dep.Spec.Template.Spec.TopologySpreadConstraints[0]
	g.Expect(tsc.MaxSkew).To(Equal(int32(1)))
	g.Expect(tsc.WhenUnsatisfiable).To(Equal(corev1.DoNotSchedule))
	g.Expect(tsc.TopologyKey).To(Equal("kubernetes.io/hostname"))
}

// TestManagerDeploymentSecurityContext validates pod and container security
// settings required for OCP and certification.
func TestManagerDeploymentSecurityContext(t *testing.T) {
	g := NewGomegaWithT(t)
	dep := parseManagerDeployment(t)

	// Pod-level security context
	podSC := dep.Spec.Template.Spec.SecurityContext
	g.Expect(podSC).NotTo(BeNil())
	g.Expect(podSC.RunAsNonRoot).To(Equal(ptr.To(true)),
		"runAsNonRoot must be true")
	g.Expect(podSC.SeccompProfile).NotTo(BeNil())
	g.Expect(podSC.SeccompProfile.Type).To(Equal(corev1.SeccompProfileTypeRuntimeDefault),
		"seccompProfile must be RuntimeDefault")

	// Container-level security context
	g.Expect(dep.Spec.Template.Spec.Containers).NotTo(BeEmpty())
	csc := dep.Spec.Template.Spec.Containers[0].SecurityContext
	g.Expect(csc).NotTo(BeNil())
	g.Expect(csc.AllowPrivilegeEscalation).To(Equal(ptr.To(false)),
		"allowPrivilegeEscalation must be false")
	g.Expect(csc.ReadOnlyRootFilesystem).To(Equal(ptr.To(true)),
		"readOnlyRootFilesystem must be true")
	g.Expect(csc.Capabilities).NotTo(BeNil())
	g.Expect(csc.Capabilities.Drop).To(ContainElement(corev1.Capability("ALL")),
		"capabilities.drop must include ALL")
}
