package e2e

import (
	"context"
	"fmt"
	"os"
	"strconv"
	"sync"
	"time"

	commonlabels "github.com/medik8s/common/pkg/labels"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	gomegatypes "github.com/onsi/gomega/types"

	v1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/labels"
	"k8s.io/apimachinery/pkg/selection"
	"sigs.k8s.io/controller-runtime/pkg/client"

	"github.com/medik8s/self-node-remediation/api/v1alpha1"
	"github.com/medik8s/self-node-remediation/controllers"
	"github.com/medik8s/self-node-remediation/e2e/utils"
)

const (
	disconnectCommand = "ip route add blackhole %s"
	reconnectCommand  = "ip route delete blackhole %s"

	// unblock API server after this time
	reconnectInterval = 300 * time.Second

	// time after which the pod should be deleted (respect API check duration!)
	podDeletedTimeout = 5 * time.Minute

	skipOOSREnvVarName = "SKIP_OOST_REMEDIATION_VERIFICATION"
)

var _ = Describe("Self Node Remediation E2E", func() {

	var apiIPs []string
	workerNodes := &v1.NodeList{}
	controlPlaneNodes := &v1.NodeList{}

	var nodeUnderTest *v1.Node
	var oldBootTime *time.Time
	var oldSnrPodName string

	BeforeEach(func() {
		if len(apiIPs) == 0 {
			// init some common stuff

			// API IPs
			apiIPs = getApiIPs()

			// Worker nodes
			selector := labels.NewSelector()
			req, _ := labels.NewRequirement(commonlabels.WorkerRole, selection.Exists, []string{})
			selector = selector.Add(*req)
			Expect(k8sClient.List(context.Background(), workerNodes, &client.ListOptions{LabelSelector: selector})).ToNot(HaveOccurred())
			Expect(len(workerNodes.Items)).To(BeNumerically(">=", 2))

			// Control plane nodes
			selector = labels.NewSelector()
			req, _ = labels.NewRequirement(commonlabels.ControlPlaneRole, selection.Exists, []string{})
			selector = selector.Add(*req)
			Expect(k8sClient.List(context.Background(), controlPlaneNodes, &client.ListOptions{LabelSelector: selector})).To(Succeed())
			Expect(len(controlPlaneNodes.Items)).To(BeNumerically(">=", 2))

		}
	})

	JustBeforeEach(func() {
		var err error
		oldBootTime, err = utils.GetBootTime(context.Background(), k8sClientSet, nodeUnderTest, testNamespace)
		Expect(err).ToNot(HaveOccurred())
		oldSnrPodName = findSnrPod(nodeUnderTest).GetName()
	})

	verifyRemediationSucceeds := func(snr *v1alpha1.SelfNodeRemediation) {
		// this does not 100% check if the pod was deleted by SNR, could be by reboot...
		checkPodDeleted(oldSnrPodName)
		utils.CheckReboot(context.Background(), k8sClientSet, nodeUnderTest, oldBootTime, testNamespace)
		// Simulate NHC deleting SNR
		deleteAndWait(snr)
		checkNoExecuteTaintRemoved(nodeUnderTest)
	}

	Describe("Workers Remediation", func() {

		BeforeEach(func() {
			nodeUnderTest = &workerNodes.Items[0]
			ensureSnrRunning(workerNodes)
		})

		Describe("With API connectivity", func() {
			// normal remediation
			// - create SNR
			// - nodeUnderTest should reboot

			var snr *v1alpha1.SelfNodeRemediation
			var remediationStrategy v1alpha1.RemediationStrategyType

			JustBeforeEach(func() {
				snr = createSNR(nodeUnderTest, remediationStrategy)
			})

			Context("Resource Deletion Strategy", func() {
				BeforeEach(func() {
					remediationStrategy = v1alpha1.ResourceDeletionRemediationStrategy
				})

				It("should remediate", func() {
					verifyRemediationSucceeds(snr)
				})
			})

			Context("OutOfService Remediation Strategy", func() {
				BeforeEach(func() {
					if _, isExist := os.LookupEnv(skipOOSREnvVarName); isExist {
						Skip("Skip this test due to out-of-service taint not supported")
					}
					remediationStrategy = v1alpha1.OutOfServiceTaintRemediationStrategy
				})

				It("should remediate", func() {
					verifyRemediationSucceeds(snr)
					checkOutOfServiceTaintRemoved(nodeUnderTest)
				})
			})
		})

		Describe("Without API connectivity", func() {
			var testStartTime *metav1.Time
			BeforeEach(func() {
				testStartTime = &metav1.Time{Time: time.Now()}
			})

			Context("Healthy node (no SNR)", func() {
				// no api connectivity
				// a) healthy
				//    - kill connectivity on one nodeUnderTest
				//    - wait until connection restored
				//    - verify nodeUnderTest did not reboot
				//    - verify peer check did happen

				BeforeEach(func() {
					killApiConnection(nodeUnderTest, apiIPs, true)
				})

				It("should not remediate", func() {
					utils.CheckNoReboot(context.Background(), k8sClientSet, nodeUnderTest, oldBootTime, testNamespace)
					checkSnrLogs(nodeUnderTest, []string{"failed to check api server", "Peer told me I'm healthy."}, testStartTime)
				})
			})

			Context("All nodes (no API connection for all)", func() {

				// no api connectivity
				// c) api issue
				//    - kill connectivity on all nodes
				//    - verify nodeUnderTest does not reboot and isn't deleted

				bootTimes := make(map[string]*time.Time)

				BeforeEach(func() {
					wg := sync.WaitGroup{}
					for i := range workerNodes.Items {
						wg.Add(1)
						worker := &workerNodes.Items[i]

						// and the last boot time
						t, err := utils.GetBootTime(context.Background(), k8sClientSet, worker, testNamespace)
						Expect(err).ToNot(HaveOccurred())
						bootTimes[worker.GetName()] = t

						go func() {
							defer GinkgoRecover()
							defer wg.Done()
							killApiConnection(worker, apiIPs, true)
						}()
					}
					wg.Wait()
					// give things a bit time to settle after API connection is restored
					time.Sleep(10 * time.Second)
				})

				It("should not remediate", func() {

					// all nodes should satisfy this test
					wg := sync.WaitGroup{}
					for i := range workerNodes.Items {
						wg.Add(1)
						worker := &workerNodes.Items[i]
						go func() {
							defer GinkgoRecover()
							defer wg.Done()
							utils.CheckNoReboot(context.Background(), k8sClientSet, worker, bootTimes[worker.GetName()], testNamespace)
							checkSnrLogs(worker, []string{"failed to check api server", "nodes couldn't access the api-server"}, testStartTime)
						}()
					}
					wg.Wait()

				})
			})

			Context("Unhealthy node (with SNR)", func() {

				// no api connectivity
				// b) unhealthy
				//    - kill connectivity on one nodeUnderTest
				//    - create SNR
				//    - verify nodeUnderTest does reboot

				var snr *v1alpha1.SelfNodeRemediation

				BeforeEach(func() {
					killApiConnection(nodeUnderTest, apiIPs, false)
					snr = createSNR(nodeUnderTest, v1alpha1.ResourceDeletionRemediationStrategy)
				})

				It("should remediate", func() {
					verifyRemediationSucceeds(snr)
					// we can't check logs of unhealthy node anymore, check peer logs
					peer := &workerNodes.Items[1]
					checkSnrLogs(peer, []string{nodeUnderTest.GetName(), "found matching SNR, node is unhealthy"}, testStartTime)
				})

			})

		})
	})

	Describe("Control Plane Remediation", func() {
		BeforeEach(func() {
			nodeUnderTest = &controlPlaneNodes.Items[0]
			ensureSnrRunning(controlPlaneNodes)
		})

		Describe("With API connectivity", func() {
			Context("creating a SNR", func() {
				// normal remediation
				// - create SNR
				// - nodeUnderTest should reboot

				var snr *v1alpha1.SelfNodeRemediation
				var remediationStrategy v1alpha1.RemediationStrategyType
				JustBeforeEach(func() {
					snr = createSNR(nodeUnderTest, remediationStrategy)
				})

				Context("Resource Deletion Strategy", func() {
					BeforeEach(func() {
						remediationStrategy = v1alpha1.ResourceDeletionRemediationStrategy
					})

					It("should remediate", func() {
						verifyRemediationSucceeds(snr)
					})
				})

			})
		})

	})
})

func checkPodDeleted(oldPodName string) bool {
	return EventuallyWithOffset(1, func() bool {
		oldPod := &v1.Pod{
			ObjectMeta: metav1.ObjectMeta{
				Name:      oldPodName,
				Namespace: testNamespace,
			},
		}
		err := k8sClient.Get(context.Background(), client.ObjectKeyFromObject(oldPod), oldPod)
		// daemonset pods are different, deletion timestamp is enough
		return errors.IsNotFound(err) || oldPod.DeletionTimestamp != nil
	}, podDeletedTimeout, 30*time.Second).Should(BeTrue())
}

func createSNR(node *v1.Node, remediationStrategy v1alpha1.RemediationStrategyType) *v1alpha1.SelfNodeRemediation {
	By("creating a SNR")
	snr := &v1alpha1.SelfNodeRemediation{
		ObjectMeta: metav1.ObjectMeta{
			Name:      node.GetName(),
			Namespace: testNamespace,
		},
		Spec: v1alpha1.SelfNodeRemediationSpec{
			RemediationStrategy: remediationStrategy,
		},
	}
	ExpectWithOffset(1, k8sClient.Create(context.Background(), snr)).ToNot(HaveOccurred())
	DeferCleanup(func() {
		_ = k8sClient.Delete(context.Background(), snr)
		Eventually(func(g Gomega) {
			err := k8sClient.Get(context.Background(), client.ObjectKeyFromObject(snr), snr)
			Expect(errors.IsNotFound(err)).To(BeTrue())
		}, 2*time.Minute, 10*time.Second).Should(Succeed())
	})
	return snr
}

func checkNoExecuteTaintRemoved(node *v1.Node) {
	By("checking if NoExecute taint was removed")
	checkTaintRemoved(node, controllers.NodeNoExecuteTaint)
}

func checkOutOfServiceTaintRemoved(node *v1.Node) {
	By("checking if out-of-service taint was removed")
	checkTaintRemoved(node, controllers.OutOfServiceTaint)
}

func checkTaintRemoved(node *v1.Node, taintToCheck *v1.Taint) {
	EventuallyWithOffset(1, func() error {
		key := client.ObjectKey{
			Name: node.GetName(),
		}
		newNode := &v1.Node{}
		if err := k8sClient.Get(context.Background(), key, newNode); err != nil {
			logger.Error(err, "error getting node")
			return err
		}
		logger.Info("", "taints", newNode.Spec.Taints)
		for _, taint := range newNode.Spec.Taints {
			if taint.MatchTaint(taintToCheck) {
				return fmt.Errorf("Taint still exists taint key:%s, taint effect:%s", taintToCheck.Key, taintToCheck.Effect)
			}
		}
		return nil
	}, 1*time.Minute, 10*time.Second).Should(Succeed())
}

func killApiConnection(node *v1.Node, apiIPs []string, withReconnect bool) {
	msg := fmt.Sprintf("killing api connectivity on NODE: %s and API ep: %v", node.Name, apiIPs)
	By(msg)

	script := composeScript(disconnectCommand, apiIPs)
	if withReconnect {
		script += fmt.Sprintf(" && sleep %s && ", strconv.Itoa(int(reconnectInterval.Seconds())))
		script += composeScript(reconnectCommand, apiIPs)
	}

	pod := findSnrPod(node)
	// ignore errors, they are expected
	result, err := utils.RunCommandInPod(context.Background(), k8sClientSet, pod, script)
	logger.Info("kill API", "result", result, "error", err)
}

func composeScript(commandTemplate string, ips []string) string {
	script := ""
	for i, ip := range ips {
		if i != 0 {
			script += " && "
		}
		script += fmt.Sprintf(commandTemplate, ip)
	}
	return script
}

func checkSnrLogs(node *v1.Node, expected []string, since *metav1.Time) {
	By("checking logs")
	pod := findSnrPod(node)
	ExpectWithOffset(1, pod).ToNot(BeNil())

	var matchers []gomegatypes.GomegaMatcher
	for _, exp := range expected {
		matchers = append(matchers, ContainSubstring(exp))
	}

	EventuallyWithOffset(1, func() string {
		var err error
		logs, err := utils.GetLogs(k8sClientSet, pod, since)
		if err != nil {
			logger.Error(err, "failed to get logs, might retry")
			return ""
		}
		return logs
	}, 6*time.Minute, 10*time.Second).Should(And(matchers...), "logs don't contain expected strings")
}

func findSnrPod(node *v1.Node) *v1.Pod {
	// long timeout, after deployment of SNR it takes some time until it is up and running
	var snrPod *v1.Pod
	EventuallyWithOffset(2, func() bool {
		pods := &v1.PodList{}
		listOptions := &client.ListOptions{
			LabelSelector: labels.SelectorFromSet(labels.Set{
				"app.kubernetes.io/name":      "self-node-remediation",
				"app.kubernetes.io/component": "agent",
			}),
		}
		err := k8sClient.List(context.Background(), pods, listOptions)
		if err != nil && !errors.IsNotFound(err) {
			logger.Error(err, "failed to list pods")
			return false
		}
		for i := range pods.Items {
			pod := pods.Items[i]
			if pod.Spec.NodeName == node.GetName() {
				snrPod = &pod
				return true
			}
		}
		return false
	}, 10*time.Minute, 30*time.Second).Should(BeTrue(), "didn't find SNR pod")
	return snrPod
}

// getApiIPs gets the IP address(es) of the default/kubernetes service, which is used for API server connections
func getApiIPs() []string {
	key := client.ObjectKey{
		Namespace: "default",
		Name:      "kubernetes",
	}
	svc := &v1.Service{}
	ExpectWithOffset(1, k8sClient.Get(context.Background(), key, svc)).ToNot(HaveOccurred())
	ips := make([]string, 0)
	for _, addr := range svc.Spec.ClusterIPs {
		ips = append(ips, addr)
	}
	return ips
}

func deleteAndWait(resource client.Object) {
	// Delete
	EventuallyWithOffset(1, func() error {
		err := k8sClient.Delete(context.Background(), resource)
		if errors.IsNotFound(err) {
			return nil
		}
		return err
	}, 2*time.Minute, 10*time.Second).ShouldNot(HaveOccurred(), "failed to delete resource")
	// Wait until deleted
	EventuallyWithOffset(1, func() bool {
		err := k8sClient.Get(context.Background(), client.ObjectKeyFromObject(resource), resource)
		if errors.IsNotFound(err) {
			return true
		}
		return false
	}, 4*time.Minute, 10*time.Second).Should(BeTrue(), "resource not deleted in time")
}

func ensureSnrRunning(nodes *v1.NodeList) {
	wg := sync.WaitGroup{}
	for i := range nodes.Items {
		wg.Add(1)
		node := &nodes.Items[i]
		go func() {
			defer GinkgoRecover()
			defer wg.Done()
			pod := findSnrPod(node)
			utils.WaitForPodReady(k8sClient, pod)
		}()
	}
	wg.Wait()
}
