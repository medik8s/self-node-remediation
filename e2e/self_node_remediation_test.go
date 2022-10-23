package e2e

import (
	"context"
	"fmt"
	"strconv"
	"strings"
	"sync"
	"time"

	. "github.com/onsi/ginkgo"
	. "github.com/onsi/gomega"
	gomegatypes "github.com/onsi/gomega/types"

	v1 "k8s.io/api/core/v1"
	storagev1 "k8s.io/api/storage/v1"
	"k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/labels"
	"k8s.io/apimachinery/pkg/selection"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"

	"github.com/medik8s/self-node-remediation/api/v1alpha1"
	"github.com/medik8s/self-node-remediation/controllers"
	"github.com/medik8s/self-node-remediation/e2e/utils"
)

const (
	disconnectCommand = "ip route add blackhole %s"
	reconnectCommand  = "ip route delete blackhole %s"
	nodeExecTimeout   = 20 * time.Second
	reconnectInterval = 300 * time.Second
)

var _ = Describe("Self Node Remediation E2E", func() {

	var node *v1.Node
	workers := &v1.NodeList{}
	var oldBootTime *time.Time
	var oldUID types.UID
	var apiIPs []string

	BeforeEach(func() {

		// get all things that doesn't change once only
		if node == nil {
			// get worker node(s)
			selector := labels.NewSelector()
			req, _ := labels.NewRequirement("node-role.kubernetes.io/worker", selection.Exists, []string{})
			selector = selector.Add(*req)
			Expect(k8sClient.List(context.Background(), workers, &client.ListOptions{LabelSelector: selector})).ToNot(HaveOccurred())
			Expect(len(workers.Items)).To(BeNumerically(">=", 2))

			node = &workers.Items[0]
			oldUID = node.GetUID()

			apiIPs = getApiIPs()
		} else {
			// just update the node for getting the current UID
			Expect(k8sClient.Get(context.Background(), client.ObjectKeyFromObject(node), node)).ToNot(HaveOccurred())
			oldUID = node.GetUID()
		}

		var err error
		oldBootTime, err = getBootTime(node)
		Expect(err).ToNot(HaveOccurred())

		ensureSnrRunning(workers)
	})

	AfterEach(func() {
		// restart snr pods for resetting logs...
		restartSnrPods(workers)
	})

	JustAfterEach(func() {
		By("printing self node remediation log of healthy node")
		healthyNode := &workers.Items[1]
		pod := findSnrPod(healthyNode)
		logs, err := utils.GetLogs(k8sClientSet, pod)
		Expect(err).ToNot(HaveOccurred())
		logger.Info("logs of healthy self-node-remediation pod", "logs", logs)
	})

	Describe("With API connectivity", func() {
		Context("creating a SNR", func() {
			// normal remediation
			// - create SNR
			// - node should reboot
			// - node should be deleted and re-created

			var snr *v1alpha1.SelfNodeRemediation
			var remediationStrategy v1alpha1.RemediationStrategyType
			JustBeforeEach(func() {
				snr = createSNR(node, remediationStrategy)
			})

			AfterEach(func() {
				if snr != nil {
					deleteAndWait(snr)
				}
			})

			Context("Resource Deletion Strategy", func() {
				var oldPodCreationTime time.Time
				var va *storagev1.VolumeAttachment

				BeforeEach(func() {
					remediationStrategy = v1alpha1.ResourceDeletionRemediationStrategy
					oldPodCreationTime = findSnrPod(node).CreationTimestamp.Time
					va = createVolumeAttachment(node)
				})

				It("should delete pods and volume attachments", func() {
					checkPodRecreated(node, oldPodCreationTime)
					checkVaDeleted(va)
					//Simulate NHC trying to delete SNR
					deleteAndWait(snr)
					snr = nil

					checkNoExecuteTaintRemoved(node)
				})
			})

			Context("Node Deletion Strategy", func() {
				BeforeEach(func() {
					remediationStrategy = v1alpha1.NodeDeletionRemediationStrategy
				})

				It("should reboot and re-create node", func() {
					// order matters
					// - because the 2nd check has a small timeout only
					checkNodeRecreate(node, oldUID)
					checkReboot(node, oldBootTime)
					checkNoExecuteTaintRemoved(node)
				})
			})
		})
	})

	Describe("Without API connectivity", func() {
		Context("Healthy node (no SNR)", func() {

			// no api connectivity
			// a) healthy
			//    - kill connectivity on one node
			//    - wait until connection restored
			//    - verify node did not reboot and wasn't deleted
			//    - verify peer check did happen

			BeforeEach(func() {
				killApiConnection(node, apiIPs, true)
			})

			AfterEach(func() {
				// nothing to do
			})

			It("should not reboot and not re-create node", func() {
				// order matters
				// - because the 2nd check has a small timeout only
				checkNoNodeRecreate(node, oldUID)
				checkNoReboot(node, oldBootTime)

				// check logs to make sure that the actual peer health check did run
				checkSnrLogs(node, []string{"failed to check api server", "Peer told me I'm healthy."})
			})
		})

		Context("Unhealthy node (with SNR)", func() {

			// no api connectivity
			// b) unhealthy
			//    - kill connectivity on one node
			//    - create SNR
			//    - verify node does reboot and and is deleted / re-created

			var snr *v1alpha1.SelfNodeRemediation

			BeforeEach(func() {
				killApiConnection(node, apiIPs, false)
				snr = createSNR(node, v1alpha1.NodeDeletionRemediationStrategy)
			})

			AfterEach(func() {
				if snr != nil {
					deleteAndWait(snr)
				}
			})

			It("should reboot and re-create node", func() {
				// order matters
				// - because node check works while api is disconnected from node, reboot check not
				// - because the 2nd check has a small timeout only
				checkNodeRecreate(node, oldUID)
				checkReboot(node, oldBootTime)

				// we can't check logs of unhealthy node anymore, check peer logs
				peer := &workers.Items[1]
				checkSnrLogs(peer, []string{node.GetName(), "node is unhealthy"})
			})

		})

		Context("All nodes (no API connection for all)", func() {

			// no api connectivity
			// c) api issue
			//    - kill connectivity on all nodes
			//    - verify node does not reboot and isn't deleted

			uids := make(map[string]types.UID)
			bootTimes := make(map[string]*time.Time)

			BeforeEach(func() {
				wg := sync.WaitGroup{}
				for i := range workers.Items {
					wg.Add(1)
					worker := &workers.Items[i]

					// save old UID first
					Expect(k8sClient.Get(context.Background(), client.ObjectKeyFromObject(worker), worker)).ToNot(HaveOccurred())
					uids[worker.GetName()] = worker.GetUID()

					// and the lat boot time
					t, err := getBootTime(worker)
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

			AfterEach(func() {
				// nothing to do
			})

			It("should not have rebooted and not be re-created", func() {

				// all nodes should satisfy this test
				wg := sync.WaitGroup{}
				for i := range workers.Items {
					wg.Add(1)
					worker := &workers.Items[i]
					go func() {
						defer GinkgoRecover()
						defer wg.Done()

						// order matters
						// - because the 2nd check has a small timeout only
						checkNoNodeRecreate(worker, uids[worker.GetName()])
						checkNoReboot(worker, bootTimes[worker.GetName()])

						// check logs to make sure that the actual peer health check did run
						checkSnrLogs(worker, []string{"failed to check api server", "nodes couldn't access the api-server"})
					}()
				}
				wg.Wait()

			})
		})
	})
})

func checkVaDeleted(va *storagev1.VolumeAttachment) {
	EventuallyWithOffset(1, func() bool {
		newVa := &storagev1.VolumeAttachment{}
		err := k8sClient.Get(context.Background(), client.ObjectKeyFromObject(va), newVa)
		return errors.IsNotFound(err)

	}, 10*time.Second, 250*time.Millisecond).Should(BeTrue())
}

func createVolumeAttachment(node *v1.Node) *storagev1.VolumeAttachment {
	foo := "foo"
	va := &storagev1.VolumeAttachment{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "some-va",
			Namespace: testNamespace,
		},
		Spec: storagev1.VolumeAttachmentSpec{
			Attacher: foo,
			Source:   storagev1.VolumeAttachmentSource{},
			NodeName: node.Name,
		},
	}

	va.Spec.Source.PersistentVolumeName = &foo
	ExpectWithOffset(1, k8sClient.Create(context.Background(), va)).To(Succeed())
	return va
}

func checkPodRecreated(node *v1.Node, oldPodCreationTime time.Time) bool {
	return EventuallyWithOffset(1, func() time.Time {
		pod := findSnrPod(node)
		return pod.CreationTimestamp.Time

	}, 7*time.Minute, 10*time.Second).Should(BeTemporally(">", oldPodCreationTime))
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
	return snr
}

func getBootTime(node *v1.Node) (*time.Time, error) {
	bootTimeCommand := []string{"uptime", "-s"}
	var bootTime time.Time
	Eventually(func() error {
		ctx, cancel := context.WithTimeout(context.Background(), nodeExecTimeout)
		defer cancel()
		bootTimeString, err := utils.ExecCommandOnNode(k8sClient, bootTimeCommand, node, ctx)
		if err != nil {
			return err
		}
		bootTime, err = time.Parse("2006-01-02 15:04:05", bootTimeString)
		if err != nil {
			return err
		}
		return nil
	}, 6*nodeExecTimeout, 10*time.Second).ShouldNot(HaveOccurred())
	return &bootTime, nil
}

func checkNodeRecreate(node *v1.Node, oldUID types.UID) {
	By("checking if node was recreated")
	logger.Info("UID", "old", oldUID)
	EventuallyWithOffset(1, func() types.UID {
		key := client.ObjectKey{
			Name: node.GetName(),
		}
		newNode := &v1.Node{}
		if err := k8sClient.Get(context.Background(), key, newNode); err != nil {
			logger.Error(err, "error getting node")
			return oldUID
		}
		newUID := newNode.GetUID()
		logger.Info("UID", "new", newUID)
		return newUID
	}, 7*time.Minute, 10*time.Second).ShouldNot(Equal(oldUID))
}

func checkNoExecuteTaintRemoved(node *v1.Node) {
	By("checking if NoExecute taint was removed")
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
			if taint.MatchTaint(controllers.NodeNoExecuteTaint) {
				return fmt.Errorf("NoExecute taint still exists")
			}
		}
		return nil
	}, 1*time.Minute, 10*time.Second).Should(Succeed())
}

func checkReboot(node *v1.Node, oldBootTime *time.Time) {
	By("checking reboot")
	logger.Info("boot time", "old", oldBootTime)
	// Note: short timeout only because this check runs after node re-create check,
	// where already multiple minute were spent
	EventuallyWithOffset(1, func() time.Time {
		newBootTime, err := getBootTime(node)
		if err != nil {
			return time.Time{}
		}
		logger.Info("boot time", "new", newBootTime)
		return *newBootTime
	}, 2*time.Minute, 10*time.Second).Should(BeTemporally(">", *oldBootTime))
}

func killApiConnection(node *v1.Node, apiIPs []string, withReconnect bool) {
	By("killing api connectivity")

	script := composeScript(disconnectCommand, apiIPs)
	if withReconnect {
		script += fmt.Sprintf(" && sleep %s && ", strconv.Itoa(int(reconnectInterval.Seconds())))
		script += composeScript(reconnectCommand, apiIPs)
	}

	command := []string{"/bin/bash", "-c", script}

	var ctx context.Context
	var cancel context.CancelFunc
	if withReconnect {
		ctx, cancel = context.WithTimeout(context.Background(), reconnectInterval+nodeExecTimeout)
	} else {
		ctx, cancel = context.WithTimeout(context.Background(), nodeExecTimeout)
	}
	defer cancel()
	_, err := utils.ExecCommandOnNode(k8sClient, command, node, ctx)

	if withReconnect {
		//in case the sleep didn't work
		deadline, _ := ctx.Deadline()
		EventuallyWithOffset(1, func() bool {
			return time.Now().After(deadline)
		}, reconnectInterval+nodeExecTimeout+time.Second, 1*time.Second).Should(BeTrue())
	}

	// deadline exceeded is ok... the command does not return because of the killed connection
	Expect(err).To(
		Or(
			Not(HaveOccurred()),
			WithTransform(func(err error) string { return err.Error() },
				ContainSubstring("deadline exceeded"),
			),
		),
	)
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

func checkNoNodeRecreate(node *v1.Node, oldUID types.UID) {
	By("checking if node was recreated")
	logger.Info("UID", "old", oldUID)
	ExpectWithOffset(1, k8sClient.Get(context.Background(), client.ObjectKeyFromObject(node), node)).ToNot(HaveOccurred())
	Expect(node.UID).To(Equal(oldUID))
}

func checkNoReboot(node *v1.Node, oldBootTime *time.Time) {
	By("checking no reboot")
	logger.Info("boot time", "old", oldBootTime)
	// Note: short timeout because this check runs after api connection was restored,
	// and multiple minutes were spent already on this test
	// we still need Eventually because getting the boot time might still fail after fiddling with api connectivity
	EventuallyWithOffset(1, func() time.Time {
		newBootTime, err := getBootTime(node)
		if err != nil {
			logger.Error(err, "failed to get boot time, might retry")
			return time.Time{}
		}
		logger.Info("boot time", "new", newBootTime)
		return *newBootTime
	}, 1*time.Minute, 10*time.Second).Should(BeTemporally("==", *oldBootTime))
}

func checkSnrLogs(node *v1.Node, expected []string) {
	By("checking logs")
	pod := findSnrPod(node)
	ExpectWithOffset(1, pod).ToNot(BeNil())

	var matchers []gomegatypes.GomegaMatcher
	for _, exp := range expected {
		matchers = append(matchers, ContainSubstring(exp))
	}

	EventuallyWithOffset(1, func() string {
		var err error
		logs, err := utils.GetLogs(k8sClientSet, pod)
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
		err := k8sClient.List(context.Background(), pods)
		if err != nil && !errors.IsNotFound(err) {
			logger.Error(err, "failed to list pods")
			return false
		}
		for i := range pods.Items {
			pod := pods.Items[i]
			if strings.HasPrefix(pod.GetName(), "self-node-remediation-ds") && pod.Spec.NodeName == node.GetName() {
				snrPod = &pod
				return true
			}
		}
		return false
	}, 9*time.Minute, 10*time.Second).Should(BeTrue(), "didn't find SNR pod")
	return snrPod
}

func restartSnrPods(nodes *v1.NodeList) {
	wg := sync.WaitGroup{}
	for i := range nodes.Items {
		wg.Add(1)
		node := &nodes.Items[i]
		go func() {
			defer GinkgoRecover()
			defer wg.Done()
			restartSnrPod(node)
		}()
	}
	wg.Wait()
}

func restartSnrPod(node *v1.Node) {
	By("restarting snr pod for resetting logs")
	pod := findSnrPod(node)
	ExpectWithOffset(1, pod).ToNot(BeNil())
	oldPodUID := pod.GetUID()

	deleteAndWait(pod)

	// wait for restart
	var newPod *v1.Pod
	EventuallyWithOffset(1, func() types.UID {
		newPod = findSnrPod(node)
		if newPod == nil {
			return oldPodUID
		}
		return newPod.GetUID()
	}, 2*time.Minute, 10*time.Second).ShouldNot(Equal(oldPodUID))

	utils.WaitForPodReady(k8sClient, newPod)
}

func getApiIPs() []string {
	key := client.ObjectKey{
		Namespace: "default",
		Name:      "kubernetes",
	}
	ep := &v1.Endpoints{}
	ExpectWithOffset(1, k8sClient.Get(context.Background(), key, ep)).ToNot(HaveOccurred())
	ips := make([]string, 0)
	for _, addr := range ep.Subsets[0].Addresses {
		ips = append(ips, addr.IP)
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
	}, 2*time.Minute, 10*time.Second).Should(BeTrue(), "resource not deleted in time")
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
