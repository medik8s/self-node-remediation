package testcontroler

import (
	"context"
	"fmt"
	"reflect"
	"time"

	commonlabels "github.com/medik8s/common/pkg/labels"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/labels"
	"k8s.io/apimachinery/pkg/selection"
	"k8s.io/apimachinery/pkg/types"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"

	machinev1beta1 "github.com/openshift/api/machine/v1beta1"

	"github.com/medik8s/self-node-remediation/api/v1alpha1"
	"github.com/medik8s/self-node-remediation/internal/apicheck"
	"github.com/medik8s/self-node-remediation/internal/certificates"
	"github.com/medik8s/self-node-remediation/internal/controller"
	"github.com/medik8s/self-node-remediation/internal/controller/tests/shared"
	"github.com/medik8s/self-node-remediation/internal/controlplane"
	"github.com/medik8s/self-node-remediation/internal/peerhealth"
	peerspkg "github.com/medik8s/self-node-remediation/internal/peers"
	"github.com/medik8s/self-node-remediation/internal/reboot"
	"github.com/medik8s/self-node-remediation/internal/utils"
	"github.com/medik8s/self-node-remediation/internal/watchdog"
)

const (
	snrNamespace = "default"
)

var _ = Describe("SNR Controller", func() {
	var snr *v1alpha1.SelfNodeRemediation
	var remediationStrategy v1alpha1.RemediationStrategyType
	var nodeRebootCapable = "true"
	var isAdditionalSetupNeeded = false

	BeforeEach(func() {
		nodeRebootCapable = "true"
		snr = &v1alpha1.SelfNodeRemediation{}
		snr.Name = shared.UnhealthyNodeName
		snr.Namespace = snrNamespace
		snrConfig = shared.GenerateTestConfig()
		time.Sleep(time.Second * 2)

		// reset watchdog for each test!
		dummyDog.Reset()
	})

	JustBeforeEach(func() {
		if isAdditionalSetupNeeded {
			createSelfNodeRemediationPod()
			verifySelfNodeRemediationPodExist()
		}
		createConfig()
		DeferCleanup(func() {
			deleteConfig()
		})
		updateIsRebootCapable(nodeRebootCapable)
	})

	AfterEach(func() {
		k8sClient.ShouldSimulateFailure = false
		k8sClient.ShouldSimulatePodDeleteFailure = false
		isAdditionalSetupNeeded = false

		By("Restore default settings for api connectivity check")
		apiConnectivityCheckConfig.MinPeersForRemediation = shared.MinPeersForRemediation

		deleteRemediations()
		deleteSelfNodeRemediationPod()
		//clear node's state, this is important to remove taints, label etc.
		Expect(k8sClient.Update(context.Background(), getNode(shared.UnhealthyNodeName)))
		Expect(k8sClient.Update(context.Background(), getNode(shared.PeerNodeName)))
		time.Sleep(time.Second * 2)
		deleteRemediations()
		clearEvents()
		verifyCleanState()
	})

	It("check nodes exist", func() {
		By("Check the unhealthy node exists")
		node := &corev1.Node{}
		Eventually(func() error {
			return k8sClient.Client.Get(context.TODO(), unhealthyNodeNamespacedName, node)
		}, 10*time.Second, 250*time.Millisecond).Should(BeNil())
		Expect(node.Name).To(Equal(shared.UnhealthyNodeName))
		Expect(node.CreationTimestamp).ToNot(BeZero())

		By("Check the peer node exists")
		node = &corev1.Node{}
		Eventually(func() error {
			return k8sClient.Client.Get(context.TODO(), peerNodeNamespacedName, node)
		}, 10*time.Second, 250*time.Millisecond).Should(BeNil())
		Expect(node.Name).To(Equal(shared.PeerNodeName))
		Expect(node.CreationTimestamp).ToNot(BeZero())
	})

	Context("Unhealthy node without self-node-remediation pod", func() {
		//if the unhealthy node doesn't have the self-node-remediation pod
		//we don't want to delete the node, since it might never
		//be in a safe state (i.e. rebooted)

		Context("ResourceDeletion strategy", func() {
			BeforeEach(func() {
				createSNR(snr, v1alpha1.ResourceDeletionRemediationStrategy)
			})

			It("snr should not have finalizers", func() {
				testNoFinalizer(snr)
			})
		})

		Context("OutOfServiceTaint strategy", func() {
			BeforeEach(func() {
				createSNR(snr, v1alpha1.OutOfServiceTaintRemediationStrategy)
			})

			It("snr should not have finalizers", func() {
				testNoFinalizer(snr)
			})
		})
	})

	Context("Unhealthy node with self-node-remediation pod but unable to reboot", func() {
		//if the unhealthy node doesn't have watchdog and it's is-reboot-capable annotation is not true
		//we don't want to delete the node, since it will never
		//be in a safe state (i.e. rebooted)

		Context("simulate daemonset pods assigned to nodes", func() {
			//since we don't have a scheduler in test, we need to do its work and create pp pod for that node

			BeforeEach(func() {
				createSNR(snr, v1alpha1.ResourceDeletionRemediationStrategy)
			})

			Context("node doesn't have is-reboot-capable annotation", func() {
				BeforeEach(func() {
					//remove the annotation, if exists
					deleteIsRebootCapableAnnotation()
				})

				It("snr should not have finalizers when is-reboot-capable annotation doesn't exist", func() {
					testNoFinalizer(snr)
				})
			})

			Context("node's is-reboot-capable annotation is false", func() {
				BeforeEach(func() {
					nodeRebootCapable = "false"
				})

				It("snr should not have finalizers when is-reboot-capable annotation is false", func() {
					testNoFinalizer(snr)
				})
			})
		})
	})

	Context("Unhealthy node with api-server access", func() {

		BeforeEach(func() {
			isAdditionalSetupNeeded = true
		})

		Context("Automatic strategy - ResourceDeletion selected", func() {

			BeforeEach(func() {
				prevVal := utils.IsOutOfServiceTaintGA
				utils.IsOutOfServiceTaintGA = false
				DeferCleanup(func() { utils.IsOutOfServiceTaintGA = prevVal })
			})

			JustBeforeEach(func() {
				createSNR(snr, v1alpha1.AutomaticRemediationStrategy)
			})

			It("Remediation flow", func() {
				verifyEvent("Normal", "RemediationStarted", "Remediation started by SNR manager")

				verifyTypeConditions(snr, metav1.ConditionTrue, metav1.ConditionUnknown, "RemediationStarted")

				verifyTimeHasBeenRebootedExists(snr)

				verifyWatchdogTriggered()

				verifyEvent("Normal", "NodeReboot", "Remediation process - about to attempt fencing the unhealthy node by rebooting it")

				verifySelfNodeRemediationPodDoesntExist()

				verifyEvent("Normal", "DeleteResources", "Remediation process - finished deleting unhealthy node resources")

				verifyFinalizerExists(snr)

				verifyEvent("Normal", "AddFinalizer", "Remediation process - successful adding finalizer")

				verifyNoScheduleTaintExist()

				verifyEvent("Normal", "AddNoScheduleTaint", "Remediation process - NoSchedule taint added to the unhealthy node")

				verifyTypeConditions(snr, metav1.ConditionFalse, metav1.ConditionTrue, "RemediationFinishedSuccessfully")

				deleteSNR(snr)

				verifyNoScheduleTaintRemoved()

				verifyEvent("Normal", "RemoveNoScheduleTaint", "Remediation process - remove NoSchedule taint from healthy remediated node")

				verifyEvent("Normal", "RemoveFinalizer", "Remediation process - remove finalizer from snr")

				verifySNRDoesNotExists(snr)

				verifyNoEvent("Normal", "AddOutOfService", "Remediation process - add OutOfService taint to unhealthy node")
				verifyNoEvent("Normal", "RemoveOutOfService", "Remediation process - remove OutOfService taint from node")

			})

			It("The snr agent attempts to keep deleting node resources during temporary api-server failure", func() {
				k8sClient.ShouldSimulatePodDeleteFailure = true

				verifyTypeConditions(snr, metav1.ConditionTrue, metav1.ConditionUnknown, "RemediationStarted")

				verifyTimeHasBeenRebootedExists(snr)

				verifyWatchdogTriggered()

				// The kube-api calls for VA fail intentionally. In this case, we expect the snr agent to try
				// to delete node resources again. So LastError is set to the error every time Reconcile()
				// is triggered. If it becomes another error, it means something unintended happens.
				verifyLastErrorKeepsApiError(snr)

				k8sClient.ShouldSimulatePodDeleteFailure = false

				verifySelfNodeRemediationPodDoesntExist()

				deleteSNR(snr)

				verifySNRDoesNotExists(snr)

			})
			When("Node isn't found", func() {
				BeforeEach(func() {
					snr.Name = "non-existing-node"
				})

				It("remediation should stop and update conditions", func() {
					verifyTypeConditions(snr, metav1.ConditionFalse, metav1.ConditionFalse, "RemediationSkippedNodeNotFound")
					verifyEvent("Warning", "RemediationCannotStart", "Could not get remediation target Node")
				})
			})

			When("Node has exclude form remediation label", func() {
				BeforeEach(func() {
					node := &corev1.Node{}
					Expect(k8sClient.Client.Get(context.TODO(), unhealthyNodeNamespacedName, node)).To(Succeed())
					node.Labels["remediation.medik8s.io/exclude-from-remediation"] = "true"
					Expect(k8sClient.Client.Update(context.TODO(), node)).To(Succeed())
					DeferCleanup(
						func() {
							node := &corev1.Node{}
							Expect(k8sClient.Client.Get(context.TODO(), unhealthyNodeNamespacedName, node)).To(Succeed())
							delete(node.Labels, "remediation.medik8s.io/exclude-from-remediation")
							Expect(k8sClient.Client.Update(context.TODO(), node)).To(Succeed())
						},
					)
				})

				It("remediation should stop", func() {
					time.Sleep(time.Second)
					verifyEvent("Normal", "RemediationSkipped", "remediation skipped this node is excluded from remediation")
				})
			})
		})

		Context("Automatic strategy - OutOfServiceTaint selected", func() {
			BeforeEach(func() {
				remediationStrategy = v1alpha1.AutomaticRemediationStrategy
				prevVal := utils.IsOutOfServiceTaintGA
				utils.IsOutOfServiceTaintGA = true
				DeferCleanup(func() { utils.IsOutOfServiceTaintGA = prevVal })
			})

			DescribeTable("Remediation flow",
				Entry("Multi same kind remediation enabled", true),
				Entry("Multi same kind remediation disabled", false),
				func(isMultiSameKindRemediationEnabled bool) {
					if isMultiSameKindRemediationEnabled {
						createSNRSameKindSupported(snr, remediationStrategy)
					} else {
						createSNR(snr, remediationStrategy)
					}
					verifyTypeConditions(snr, metav1.ConditionTrue, metav1.ConditionUnknown, "RemediationStarted")

					// The normal NoSchedule taint stops new workloads from being scheduled on the node.
					createTerminatingPod()

					verifyTimeHasBeenRebootedExists(snr)

					verifyWatchdogTriggered()

					verifyFinalizerExists(snr)

					verifyNoScheduleTaintExist()

					verifyOutOfServiceTaintExist()

					verifyEvent("Normal", "AddOutOfService", "Remediation process - add out-of-service taint to unhealthy node")

					// simulate the out-of-service taint by Pod GC Controller
					deleteTerminatingPod()

					verifyOutOfServiceTaintRemoved()

					verifyEvent("Normal", "RemoveOutOfService", "Remediation process - remove out-of-service taint from node")

					verifyTypeConditions(snr, metav1.ConditionFalse, metav1.ConditionTrue, "RemediationFinishedSuccessfully")

					deleteSNR(snr)

					verifyNoScheduleTaintRemoved()

					verifySNRDoesNotExists(snr)
				})

			It("should automatically remove out-of-service taint after timeout when workloads are not deleted", func() {
				// Temporarily set a much shorter timeout for testing (instead of 1 minute)
				originalTimeout := controller.OutOfServiceTimeoutDuration
				controller.OutOfServiceTimeoutDuration = 3 * time.Second
				DeferCleanup(func() {
					controller.OutOfServiceTimeoutDuration = originalTimeout
				})

				createSNR(snr, remediationStrategy)
				verifyTypeConditions(snr, metav1.ConditionTrue, metav1.ConditionUnknown, "RemediationStarted")

				// Create terminating pod but DON'T delete it - this simulates workloads stuck in terminating state
				createTerminatingPod()

				verifyTimeHasBeenRebootedExists(snr)
				verifyWatchdogTriggered()
				verifyFinalizerExists(snr)
				verifyNoScheduleTaintExist()
				verifyOutOfServiceTaintExist()

				// Verify timestamp annotation was added
				nodeKey := types.NamespacedName{Name: shared.UnhealthyNodeName}
				updatedNode := &corev1.Node{}
				verifyEvent("Normal", "AddOutOfService", "Remediation process - add out-of-service taint to unhealthy node")

				// Simulate NHC trying to delete SNR because the node is healthy
				deleteSNR(snr)
				// Wait for less than timeout (1 second) and verify taint still exists
				time.Sleep(1 * time.Second)
				verifyOutOfServiceTaintExist()

				// Wait for timeout to expire (3 seconds) and verify taint is automatically removed
				Eventually(func() bool {
					err := k8sClient.Get(context.Background(), nodeKey, updatedNode)
					if err != nil {
						return false
					}
					return !utils.TaintExists(updatedNode.Spec.Taints, &corev1.Taint{
						Key:    corev1.TaintNodeOutOfService,
						Value:  "nodeshutdown",
						Effect: corev1.TaintEffectNoExecute,
					})
				}, 10*time.Second, 250*time.Millisecond).Should(BeTrue(), "out-of-service taint should be automatically removed after 3 second timeout")

				// Verify warning event was emitted for timeout expiration
				verifyEvent("Warning", "OutOfServiceTimestampExpired", "Out-of-service taint automatically removed due to timeout expiration on a healthy node")

				// Clean up: manually delete the terminating pod and SNR to complete the test
				deleteTerminatingPod()
				verifyNoScheduleTaintRemoved()
				verifySNRDoesNotExists(snr)
			})
		})

		Context("Remediation has a Machine Owner Ref", func() {
			var machine *machinev1beta1.Machine
			var machineName = "test-machine"
			BeforeEach(func() {
				snr.OwnerReferences = []metav1.OwnerReference{{Name: machineName, Kind: "Machine", APIVersion: "machine.openshift.io/v1beta1", UID: "12345"}}
			})

			JustBeforeEach(func() {
				createSNR(snr, v1alpha1.AutomaticRemediationStrategy)
			})

			When("A machine exist that matches the remediation OwnerRef machine", func() {
				var machineStatus *machinev1beta1.MachineStatus

				JustBeforeEach(func() {
					Expect(k8sClient.Create(context.Background(), machine)).To(Succeed())
					DeferCleanup(func() {
						savedMachine := &machinev1beta1.Machine{}
						Expect(k8sClient.Get(context.Background(), client.ObjectKeyFromObject(machine), savedMachine)).To(Succeed())
						Expect(k8sClient.Delete(context.Background(), savedMachine))
					})

					if machineStatus != nil {
						time.Sleep(time.Second)
						savedMachine := &machinev1beta1.Machine{}
						Expect(k8sClient.Get(context.Background(), client.ObjectKeyFromObject(machine), savedMachine)).To(Succeed())
						savedMachine.Status = *machineStatus
						Expect(k8sClient.Status().Update(context.Background(), savedMachine)).To(Succeed())
					}
				})
				BeforeEach(func() {
					machine = &machinev1beta1.Machine{
						ObjectMeta: metav1.ObjectMeta{
							Name:      machineName,
							Namespace: snrNamespace,
						},
					}
				})
				When("the correct NodeRef is set in the machine statusThe", func() {

					BeforeEach(func() {
						machineStatus = &machinev1beta1.MachineStatus{
							NodeRef: &corev1.ObjectReference{Name: shared.UnhealthyNodeName},
						}
						DeferCleanup(func() {
							machineStatus = nil
						})
					})

				})

				When("the wrong  NodeRef is set in the machine statusThe", func() {
					BeforeEach(func() {
						machineStatus = &machinev1beta1.MachineStatus{
							NodeRef: &corev1.ObjectReference{Name: "made-up-non-existing-node"},
						}
						DeferCleanup(func() {
							machineStatus = nil
						})
					})

					When("NHC isn't set as owner in the remediation", func() {
						It("Node is not found", func() {
							time.Sleep(time.Second)
							verifyTypeConditions(snr, metav1.ConditionFalse, metav1.ConditionFalse, "RemediationSkippedNodeNotFound")
							verifyEvent("Warning", "RemediationCannotStart", "Could not get remediation target Node")
						})
					})
					When("NHC is set as owner in the remediation", func() {
						BeforeEach(func() {
							snr.OwnerReferences = append(snr.OwnerReferences, metav1.OwnerReference{Name: "nhc", Kind: "NodeHealthCheck", APIVersion: "remediation.medik8s.io/v1alpha1", UID: "12345"})
						})
					})
				})
			})

		})
	})

	Context("Unhealthy node without api-server access", func() {
		BeforeEach(func() {
			By("Simulate api-server failure")
			k8sClient.ShouldSimulateFailure = true
			remediationStrategy = v1alpha1.ResourceDeletionRemediationStrategy
		})

		Context("no peer found", func() {
			It("Verify that watchdog is not triggered", func() {
				verifyWatchdogNotTriggered()
			})
		})

		Context("no peer found and MinPeersForRemediation is configured to 0", func() {
			BeforeEach(func() {
				By("Set MinPeersForRemedation to zero which should trigger the watchdog before the test")
				apiConnectivityCheckConfig.MinPeersForRemediation = 0
			})

			It("Does not receive peer communication and since configured to need zero peers, initiates a reboot",
				func() {
					verifyWatchdogTriggered()
				})
		})

	})

	Context("Configuration is missing", func() {
		JustBeforeEach(func() {
			//Delete it
			deleteConfig()
			//Restore the config
			DeferCleanup(func() {
				createConfig()
			})

			createSNR(snr, v1alpha1.AutomaticRemediationStrategy)
		})
		It("verify remediation updated snr status", func() {
			shared.VerifySNRStatusExist(k8sClient, snr, "Disabled", metav1.ConditionTrue)
		})
	})

	// Regression test for https://github.com/medik8s/self-node-remediation/issues/251:
	// A control-plane node whose worker-peer path yields no result (isolated from workers)
	// must still remediate when peer CP nodes have a SNR CR for it (consider it unhealthy).
	Context("Control plane node - API server down - CP peer reports it unhealthy", func() {
		const cpPeerHealthPort = 9001

		var cpDummyDog watchdog.FakeWatchdog

		BeforeEach(func() {
			// 1. Label both nodes as control-plane so the Peers selector works correctly.
			for _, nodeName := range []string{shared.UnhealthyNodeName, shared.PeerNodeName} {
				node := &corev1.Node{}
				Expect(k8sClient.Client.Get(context.Background(), client.ObjectKey{Name: nodeName}, node)).To(Succeed())
				patch := client.MergeFrom(node.DeepCopy())
				node.Labels[commonlabels.ControlPlaneRole] = ""
				Expect(k8sClient.Client.Patch(context.Background(), node, patch)).To(Succeed())
				nodeName := nodeName
				DeferCleanup(func() {
					n := &corev1.Node{}
					Expect(k8sClient.Client.Get(context.Background(), client.ObjectKey{Name: nodeName}, n)).To(Succeed())
					p := client.MergeFrom(n.DeepCopy())
					delete(n.Labels, commonlabels.ControlPlaneRole)
					Expect(k8sClient.Client.Patch(context.Background(), n, p)).To(Succeed())
				})
			}

			// 2. Generate TLS certs shared between the peerhealth server and the apiCheck client.
			caPem, certPem, keyPem, err := certificates.CreateCerts()
			Expect(err).ToNot(HaveOccurred())
			certReader := &certificates.MemoryCertStorage{
				CaPem:   caPem,
				CertPem: certPem,
				KeyPem:  keyPem,
			}

			// 3. Start a real peerhealth gRPC server on localhost representing the peer CP node.
			//    It uses the direct API reader (not the failure-simulation wrapper) so it can
			//    still list SNR CRs even when ShouldSimulateFailure is true.
			phServer, err := peerhealth.NewServer(
				k8sClient.Client,
				k8sClient.Reader,
				ctrl.Log.WithName("cp-peer-health-server"),
				cpPeerHealthPort,
				certReader,
				5*time.Second,
			)
			Expect(err).ToNot(HaveOccurred())
			serverCtx, cancelServer := context.WithCancel(context.Background())
			go func() { _ = phServer.Start(serverCtx) }()
			DeferCleanup(cancelServer)

			// 4. Create a fake SNR-agent pod on the peer node with podIP=127.0.0.1 so that
			//    GetPeersAddresses(ControlPlane) resolves to the test gRPC server above.
			cpPeerPod := &corev1.Pod{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "cp-peer-snr-pod",
					Namespace: shared.Namespace,
					Labels: map[string]string{
						"app.kubernetes.io/name":      "self-node-remediation",
						"app.kubernetes.io/component": "agent",
					},
				},
				Spec: corev1.PodSpec{
					NodeName:   shared.PeerNodeName,
					Containers: []corev1.Container{{Name: "snr", Image: "dummy"}},
				},
			}
			Expect(k8sClient.Client.Create(context.Background(), cpPeerPod)).To(Succeed())
			cpPeerPod.Status.PodIPs = []corev1.PodIP{{IP: "127.0.0.1"}}
			Expect(k8sClient.Client.Status().Update(context.Background(), cpPeerPod)).To(Succeed())
			DeferCleanup(func() {
				_ = k8sClient.Client.Delete(context.Background(), cpPeerPod)
			})

			// 5. Create the SNR CR for the unhealthy CP node so the peerhealth server returns Unhealthy.
			createSNR(snr, v1alpha1.ResourceDeletionRemediationStrategy)
			DeferCleanup(func() { deleteSNR(snr) })

			// 6. Build a dedicated Peers instance for the CP apiCheck.
			//    It must be started AFTER the CP labels are applied so that
			//    controlPlanePeerSelector is built correctly.
			cpPeers := peerspkg.New(
				shared.UnhealthyNodeName,
				shared.ApiCheckInterval, // short interval so the first update happens quickly
				k8sClient.Reader,
				ctrl.Log.WithName("cp-peers"),
				5*time.Second,
			)
			cpPeersCtx, cancelCpPeers := context.WithCancel(context.Background())
			go func() { _ = cpPeers.Start(cpPeersCtx) }()
			DeferCleanup(cancelCpPeers)

			// 7. Initialise the controlplane.Manager for the unhealthy CP node and call Start
			//    while the API is still reachable.
			cpManager := controlplane.NewManager(shared.UnhealthyNodeName, k8sClient.Client)
			Expect(cpManager.Start(context.Background())).To(Succeed())

			// 8. Give Peers time to run its first update and populate controlPlanePeersAddresses.
			time.Sleep(2 * time.Second)

			// 9. Start a dedicated watchdog for the CP apiCheck and wait until it is Armed.
			//    Without calling Start(), GetTimeout() returns 0 and Reboot() skips Stop().
			cpDummyDog = watchdog.NewFake(true)
			cpDogCtx, cancelCpDog := context.WithCancel(context.Background())
			go func() { _ = cpDummyDog.Start(cpDogCtx) }()
			DeferCleanup(cancelCpDog)
			Eventually(func() bool {
				return cpDummyDog.Status() == watchdog.Armed
			}, 5*time.Second, 100*time.Millisecond).Should(BeTrue(), "cpDummyDog should reach Armed state")

			// 10. Build and start the CP apiCheck.
			//     Use an unreachable host so the REST /readyz check always fails immediately.
			//     ShouldSimulateFailure only intercepts List() calls and has no effect on
			//     the raw REST request, so a fake host is the reliable way to force failure.
			fakeCfg := *cfg
			fakeCfg.Host = "https://127.0.0.1:1"
			cpRebooter := reboot.NewWatchdogRebooter(cpDummyDog, ctrl.Log.WithName("cp-rebooter"))
			cpApiCheckCfg := &apicheck.ApiConnectivityCheckConfig{
				Log:                    ctrl.Log.WithName("cp-api-check"),
				MyNodeName:             shared.UnhealthyNodeName,
				CheckInterval:          shared.ApiCheckInterval,
				MaxErrorsThreshold:     shared.MaxErrorThreshold,
				Peers:                  cpPeers,
				Rebooter:               cpRebooter,
				Cfg:                    &fakeCfg,
				MinPeersForRemediation: 0, // forces UnHealthyBecauseNodeIsIsolated path (the bug path from issue #251)
				CertReader:             certReader,
				PeerHealthPort:         cpPeerHealthPort,
				PeerDialTimeout:        5 * time.Second,
				PeerRequestTimeout:     7 * time.Second,
				ApiServerTimeout:       5 * time.Second,
			}
			cpApiCheck := apicheck.New(cpApiCheckCfg, cpManager)
			cpCheckCtx, cancelCpCheck := context.WithCancel(context.Background())
			go func() { _ = cpApiCheck.Start(cpCheckCtx) }()
			DeferCleanup(cancelCpCheck)
		})

		It("should trigger the watchdog when CP peers report this node unhealthy (fix for issue #251)", func() {
			EventuallyWithOffset(1, func(g Gomega) {
				g.Expect(cpDummyDog.Status()).To(Equal(watchdog.Triggered))
			}, 30*time.Second, 1*time.Second).Should(Succeed(),
				"watchdog should be triggered: CP peers reported node unhealthy but watchdog was not triggered")
		})
	})
})

func verifyTypeConditions(snr *v1alpha1.SelfNodeRemediation, expectedProcessingConditionStatus, expectedSucceededConditionStatus metav1.ConditionStatus, expectedReason string) {
	By("Verify that SNR Processing status condition is correct")
	Eventually(func() bool {
		snrNamespacedName := client.ObjectKeyFromObject(snr)
		if err := k8sClient.Client.Get(context.Background(), snrNamespacedName, snr); err != nil {
			return false
		}
		actualProcessingCondition := meta.FindStatusCondition(snr.Status.Conditions, string(v1alpha1.ProcessingConditionType))
		isActualProcessingMatchExpected := actualProcessingCondition != nil && actualProcessingCondition.Status == expectedProcessingConditionStatus
		isActualSucceededMatchExpected := meta.IsStatusConditionPresentAndEqual(snr.Status.Conditions, string(v1alpha1.SucceededConditionType), expectedSucceededConditionStatus)

		return isActualProcessingMatchExpected &&
			isActualSucceededMatchExpected && actualProcessingCondition.Reason == expectedReason

	}, 5*time.Second, 250*time.Millisecond).Should(BeTrue(), "SNR Processing status condition expected %s got %v", expectedReason, snr.Status.Conditions)
}

func verifyLastErrorKeepsApiError(snr *v1alpha1.SelfNodeRemediation) {
	By("Verify that LastError in SNR status has been kept")
	EventuallyWithOffset(1, func() bool {
		snrNamespacedName := client.ObjectKeyFromObject(snr)
		if err := k8sClient.Client.Get(context.Background(), snrNamespacedName, snr); err != nil {
			return false
		}
		return snr.Status.LastError == k8sClient.SimulatedFailureMessage
	}, shared.CalculatedRebootDuration+10*time.Second, 250*time.Millisecond).Should(BeTrue())
}

func verifySelfNodeRemediationPodDoesntExist() {
	By("Verify that self node remediation pod has been deleted as part of the remediation")
	podKey := client.ObjectKey{
		Namespace: shared.Namespace,
		Name:      "self-node-remediation",
	}

	EventuallyWithOffset(1, func() bool {
		pod := &corev1.Pod{}
		err := k8sClient.Get(context.Background(), podKey, pod)
		return apierrors.IsNotFound(err)

	}, shared.CalculatedRebootDuration+10*time.Second, 250*time.Millisecond).Should(BeTrue())
}

func verifyWatchdogTriggered() {
	EventuallyWithOffset(1, func(g Gomega) {
		g.Expect(dummyDog.Status()).To(Equal(watchdog.Triggered))
	}, 5*dummyDog.GetTimeout(), 1*time.Second).Should(Succeed(), "watchdog should be triggered")
}

func verifyWatchdogNotTriggered() {
	ConsistentlyWithOffset(1, func(g Gomega) {
		g.Expect(dummyDog.Status()).To(Equal(watchdog.Armed))
	}, 5*dummyDog.GetTimeout(), 1*time.Second).Should(Succeed(), "watchdog should not be triggered")
}

func verifyFinalizerExists(snr *v1alpha1.SelfNodeRemediation) {
	By("Verify that finalizer was added")
	snrNamespacedName := client.ObjectKeyFromObject(snr)
	ExpectWithOffset(1, k8sClient.Get(context.Background(), snrNamespacedName, snr)).To(Succeed(), "failed to get snr")
	ExpectWithOffset(1, controllerutil.ContainsFinalizer(snr, controller.SNRFinalizer)).Should(BeTrue(), "finalizer should be added")
}

func verifyNoScheduleTaintRemoved() {
	By("Verify that node does not have NoSchedule taint")
	Eventually(func() (bool, error) {
		return isTaintExist(controller.NodeNoScheduleTaint)
	}, 10*time.Second, 200*time.Millisecond).Should(BeFalse())
}

func verifyNoScheduleTaintExist() {
	By("Verify that node has NoSchedule taint")
	Eventually(func() (bool, error) {
		return isTaintExist(controller.NodeNoScheduleTaint)
	}, 10*time.Second, 200*time.Millisecond).Should(BeTrue())
}

func verifyOutOfServiceTaintRemoved() {
	By("Verify that node does not have out-of-service taint")
	Eventually(func() (bool, error) {
		return isTaintExist(controller.OutOfServiceTaint)
	}, 10*time.Second, 200*time.Millisecond).Should(BeFalse())
}

func verifyOutOfServiceTaintExist() {
	By("Verify that node has out-of-service taint")
	Eventually(func() (bool, error) {
		return isTaintExist(controller.OutOfServiceTaint)
	}, shared.CalculatedRebootDuration+10*time.Second, 200*time.Millisecond).Should(BeTrue())
}

func isTaintExist(taintToMatch *corev1.Taint) (bool, error) {
	node := &corev1.Node{}
	err := k8sClient.Reader.Get(context.TODO(), unhealthyNodeNamespacedName, node)
	if err != nil {
		return false, err
	}
	for _, taint := range node.Spec.Taints {
		if taintToMatch.MatchTaint(&taint) {
			return true, nil
		}
	}
	return false, nil
}

func verifyTimeHasBeenRebootedExists(snr *v1alpha1.SelfNodeRemediation) {
	By("Verify that time has been added to SNR status")
	EventuallyWithOffset(1, func() (*metav1.Time, error) {
		snrNamespacedName := client.ObjectKeyFromObject(snr)
		err := k8sClient.Client.Get(context.Background(), snrNamespacedName, snr)
		return snr.Status.TimeAssumedRebooted, err
	}, 5*time.Second, 250*time.Millisecond).ShouldNot(BeZero())
}

func verifySNRDoesNotExists(snr *v1alpha1.SelfNodeRemediation) {
	By("Verify that SNR does not exit")
	Eventually(func() bool {
		snrNamespacedName := client.ObjectKeyFromObject(snr)
		err := k8sClient.Get(context.Background(), snrNamespacedName, snr)
		return apierrors.IsNotFound(err)
	}, 5*time.Second, 250*time.Millisecond).Should(BeTrue())
}

func verifySelfNodeRemediationPodExist() {
	podList := &corev1.PodList{}
	selector := labels.NewSelector()
	nameRequirement, _ := labels.NewRequirement("app.kubernetes.io/name", selection.Equals, []string{"self-node-remediation"})
	componentRequirement, _ := labels.NewRequirement("app.kubernetes.io/component", selection.Equals, []string{"agent"})
	selector = selector.Add(*nameRequirement, *componentRequirement)

	EventuallyWithOffset(1, func() (int, error) {
		err := k8sClient.Client.List(context.Background(), podList, &client.ListOptions{LabelSelector: selector})
		return len(podList.Items), err
	}, 5*time.Second, 250*time.Millisecond).Should(Equal(1))
}
func deleteRemediations() {

	Eventually(func(g Gomega) {
		snrs := &v1alpha1.SelfNodeRemediationList{}
		g.Expect(k8sClient.List(context.Background(), snrs)).To(Succeed())
		if len(snrs.Items) == 0 {
			return
		}

		for _, snr := range snrs.Items {
			tmpSnr := snr
			g.Expect(removeFinalizers(&tmpSnr)).To(Succeed())
			g.Expect(k8sClient.Client.Delete(context.Background(), &tmpSnr)).To(Succeed())

		}

		expectedEmptySnrs := &v1alpha1.SelfNodeRemediationList{}
		g.Expect(k8sClient.List(context.Background(), expectedEmptySnrs)).To(Succeed())
		g.Expect(len(expectedEmptySnrs.Items)).To(Equal(0))

	}, 10*time.Second, 100*time.Millisecond).Should(Succeed())
}

func deleteSNR(snr *v1alpha1.SelfNodeRemediation) {
	snrKey := client.ObjectKey{Name: snr.Name, Namespace: snr.Namespace}

	err := k8sClient.Get(context.Background(), snrKey, snr)

	if apierrors.IsNotFound(err) {
		return
	}

	Expect(err).Should(Succeed())

	Expect(k8sClient.Client.Delete(context.Background(), snr)).To(Succeed(), "failed to delete snr CR")

}

func removeFinalizers(snr *v1alpha1.SelfNodeRemediation) error {
	if len(snr.GetFinalizers()) == 0 {
		return nil
	}
	snr.SetFinalizers(nil)
	if err := k8sClient.Client.Update(context.Background(), snr); err != nil {
		return err
	}
	return nil
}

func createSNRSameKindSupported(snr *v1alpha1.SelfNodeRemediation, strategy v1alpha1.RemediationStrategyType) {
	// Assume that snr.Name contains the node name here (for testing purpose).
	// We need to add a suffix to make the name unique and add the node name as an annotation
	targetNodeName := snr.Name
	snr.Name = fmt.Sprintf("%s-%s", targetNodeName, "pseudo-random-test-suffix")
	snr.Annotations = map[string]string{"remediation.medik8s.io/node-name": targetNodeName}
	createSNR(snr, strategy)
}

func createSNR(snr *v1alpha1.SelfNodeRemediation, strategy v1alpha1.RemediationStrategyType) {
	snr.Spec.RemediationStrategy = strategy
	ExpectWithOffset(1, k8sClient.Client.Create(context.TODO(), snr)).To(Succeed(), "failed to create snr CR")
}

func createSelfNodeRemediationPod() {
	pod := &corev1.Pod{}
	pod.Spec.NodeName = shared.UnhealthyNodeName
	pod.Labels = map[string]string{"app.kubernetes.io/name": "self-node-remediation",
		"app.kubernetes.io/component": "agent"}

	pod.Name = "self-node-remediation"
	pod.Namespace = shared.Namespace
	container := corev1.Container{
		Name:  "foo",
		Image: "foo",
	}
	pod.Spec.Containers = []corev1.Container{container}
	ExpectWithOffset(1, k8sClient.Client.Create(context.Background(), pod)).To(Succeed())
}

func deleteSelfNodeRemediationPod() {
	pod := &corev1.Pod{}

	podKey := client.ObjectKey{
		Namespace: shared.Namespace,
		Name:      "self-node-remediation",
	}

	if err := k8sClient.Get(context.Background(), podKey, pod); err != nil {
		Expect(apierrors.IsNotFound(err)).To(BeTrue())
		return
	}

	var grace client.GracePeriodSeconds = 0
	ExpectWithOffset(1, k8sClient.Client.Delete(context.Background(), pod, grace)).To(Succeed())

	EventuallyWithOffset(1, func() bool {
		err := k8sClient.Client.Get(context.Background(), podKey, pod)
		return apierrors.IsNotFound(err)
	}, 10*time.Second, 100*time.Millisecond).Should(BeTrue())
}

func createTerminatingPod() {
	pod := &corev1.Pod{}
	pod.Spec.NodeName = shared.UnhealthyNodeName
	pod.Name = "terminatingpod"
	pod.Namespace = "default"
	container := corev1.Container{
		Name:  "bar",
		Image: "bar",
	}
	pod.Spec.Containers = []corev1.Container{container}
	pod.ObjectMeta = metav1.ObjectMeta{Name: pod.Name, Namespace: pod.Namespace, Finalizers: []string{"medik8s.io/keep-me"}}
	ExpectWithOffset(1, k8sClient.Client.Create(context.Background(), pod)).To(Succeed())
	ExpectWithOffset(1, k8sClient.Client.Delete(context.Background(), pod)).To(Succeed())
}

func deleteTerminatingPod() {
	pod := &corev1.Pod{}
	podKey := client.ObjectKey{
		Name:      "terminatingpod",
		Namespace: "default",
	}

	// remove finalizer to allow pod deletion
	ExpectWithOffset(1, k8sClient.Client.Get(context.Background(), podKey, pod)).To(Succeed())
	pod.Finalizers = []string{}
	ExpectWithOffset(1, k8sClient.Client.Update(context.Background(), pod)).To(Succeed())

	var grace client.GracePeriodSeconds = 0
	ExpectWithOffset(1, k8sClient.Client.Delete(context.Background(), pod, grace)).To(Succeed())

	EventuallyWithOffset(1, func() bool {
		err := k8sClient.Client.Get(context.Background(), podKey, pod)
		return apierrors.IsNotFound(err)
	}, 10*time.Second, 100*time.Millisecond).Should(BeTrue())
}

func updateIsRebootCapable(rebootCapableAnnotationValue string) {
	unhealthyNodeKey := types.NamespacedName{
		Name: shared.UnhealthyNodeName,
	}
	node := &corev1.Node{}
	ExpectWithOffset(1, k8sClient.Client.Get(context.Background(), unhealthyNodeKey, node)).To(Succeed())
	patch := client.MergeFrom(node.DeepCopy())
	if node.Annotations == nil {
		node.Annotations = map[string]string{}
	}
	if rebootCapableAnnotationValue != "" {
		node.Annotations[utils.IsRebootCapableAnnotation] = rebootCapableAnnotationValue
	}

	ExpectWithOffset(1, k8sClient.Client.Patch(context.Background(), node, patch)).To(Succeed())
}

func deleteIsRebootCapableAnnotation() {
	unhealthyNodeKey := types.NamespacedName{
		Name: shared.UnhealthyNodeName,
	}
	unhealthyNode := &corev1.Node{}
	ExpectWithOffset(1, k8sClient.Client.Get(context.Background(), unhealthyNodeKey, unhealthyNode)).To(Succeed())
	patch := client.MergeFrom(unhealthyNode.DeepCopy())
	if unhealthyNode.Annotations != nil {
		delete(unhealthyNode.Annotations, utils.IsRebootCapableAnnotation)
	}

	ExpectWithOffset(1, k8sClient.Client.Patch(context.Background(), unhealthyNode, patch)).To(Succeed())
}

// testNoFinalizer checks that snr doesn't have finalizer
func testNoFinalizer(snr *v1alpha1.SelfNodeRemediation) {
	snrKey := client.ObjectKeyFromObject(snr)

	EventuallyWithOffset(1, func() ([]string, error) {
		err := k8sClient.Client.Get(context.Background(), snrKey, snr)
		return snr.Finalizers, err
	}, 10*time.Second, 200*time.Millisecond).Should(BeEmpty())

	ConsistentlyWithOffset(1, func() ([]string, error) {
		err := k8sClient.Client.Get(context.Background(), snrKey, snr)
		//if no finalizer was set, it means we didn't start remediation process
		return snr.Finalizers, err
	}, 10*time.Second, 250*time.Millisecond).Should(BeEmpty())
}

func eventuallyUpdateNode(updateFunc func(*corev1.Node), isStatusUpdate bool) {
	By("Verify that node was updated successfully")

	EventuallyWithOffset(1, func() error {
		node := &corev1.Node{}
		if err := k8sClient.Client.Get(context.TODO(), unhealthyNodeNamespacedName, node); err != nil {
			return err
		}
		updateFunc(node)
		if isStatusUpdate {
			return k8sClient.Client.Status().Update(context.TODO(), node)
		}
		return k8sClient.Client.Update(context.TODO(), node)
	}, 5*time.Second, 250*time.Millisecond).Should(Succeed())

}

func verifyCleanState() {
	//Verify nodes are at a clean state
	nodes := &corev1.NodeList{}
	Expect(k8sClient.List(context.Background(), nodes)).To(Succeed())
	Expect(len(nodes.Items)).To(BeEquivalentTo(2))
	var peerNodeActual, unhealthyNodeActual *corev1.Node
	if nodes.Items[0].Name == shared.UnhealthyNodeName {
		Expect(nodes.Items[1].Name).To(Equal(shared.PeerNodeName))
		peerNodeActual = &nodes.Items[1]
		unhealthyNodeActual = &nodes.Items[0]
	} else {
		Expect(nodes.Items[0].Name).To(Equal(shared.PeerNodeName))
		Expect(nodes.Items[1].Name).To(Equal(shared.UnhealthyNodeName))
		peerNodeActual = &nodes.Items[0]
		unhealthyNodeActual = &nodes.Items[1]
	}

	peerNodeExpected, unhealthyNodeExpected := getNode(shared.PeerNodeName), getNode(shared.UnhealthyNodeName)
	verifyNodesAreEqual(peerNodeExpected, peerNodeActual)
	verifyNodesAreEqual(unhealthyNodeExpected, unhealthyNodeActual)

	//Verify no existing remediations
	remediations := &v1alpha1.SelfNodeRemediationList{}
	Expect(k8sClient.List(context.Background(), remediations)).To(Succeed())
	Expect(len(remediations.Items)).To(BeEquivalentTo(0))

	//Verify SNR Pod Does not exist
	pod := &corev1.Pod{}
	podKey := client.ObjectKey{
		Namespace: shared.Namespace,
		Name:      "self-node-remediation",
	}
	err := k8sClient.Get(context.Background(), podKey, pod)
	Expect(apierrors.IsNotFound(err)).To(BeTrue())

	verifyOutOfServiceTaintRemoved()

}

func verifyNodesAreEqual(expected *corev1.Node, actual *corev1.Node) {
	Expect(expected.Name).To(Equal(actual.Name))
	Expect(reflect.DeepEqual(expected.Spec, actual.Spec)).To(BeTrue())
	Expect(reflect.DeepEqual(expected.Status, actual.Status)).To(BeTrue())
	Expect(reflect.DeepEqual(expected.Annotations, actual.Annotations)).To(BeTrue())
	Expect(reflect.DeepEqual(expected.Labels, actual.Labels)).To(BeTrue())
}

func clearEvents() {
	for {
		select {
		case _ = <-fakeRecorder.Events:

		default:
			return
		}
	}
}

func verifyEvent(eventType, reason, message string) {
	EventuallyWithOffset(1, func() bool {
		return isEventOccurred(eventType, reason, message)
	}, 5*time.Second, 250*time.Millisecond).Should(BeTrue())
}

func verifyNoEvent(eventType, reason, message string) {
	ConsistentlyWithOffset(1, func() bool {
		return isEventOccurred(eventType, reason, message)
	}, 5*time.Second, 250*time.Millisecond).Should(BeFalse())
}

func isEventOccurred(eventType string, reason string, message string) bool {
	expected := fmt.Sprintf("%s %s [remediation] %s", eventType, reason, message)
	isEventMatch := false

	unMatchedEvents := make(chan string, len(fakeRecorder.Events))
	By(fmt.Sprintf("verifying that the event was: %s", expected))
	isDone := false
	for {
		select {
		case event := <-fakeRecorder.Events:
			if isEventMatch = event == expected; isEventMatch {
				isDone = true
			} else {
				unMatchedEvents <- event
			}
		default:
			isDone = true
		}
		if isDone {
			break
		}
	}

	close(unMatchedEvents)
	for unMatchedEvent := range unMatchedEvents {
		fakeRecorder.Events <- unMatchedEvent
	}
	return isEventMatch
}

func deleteConfig() {
	snrConfigTmp := &v1alpha1.SelfNodeRemediationConfig{}
	// make sure config is already created
	Eventually(func(g Gomega) {
		g.Expect(k8sClient.Get(context.Background(), client.ObjectKeyFromObject(snrConfig), snrConfigTmp)).To(Succeed())
	}, 10*time.Second, 100*time.Millisecond).Should(Succeed())

	//delete config and verify it's deleted
	Expect(k8sClient.Delete(context.Background(), snrConfigTmp)).To(Succeed())
	Eventually(func(g Gomega) {
		err := k8sClient.Get(context.Background(), client.ObjectKeyFromObject(snrConfig), snrConfigTmp)
		g.Expect(apierrors.IsNotFound(err)).To(BeTrue())
	}, 10*time.Second, 100*time.Millisecond).Should(Succeed())

}

func createConfig() {
	snrConfigTmp := snrConfig.DeepCopy()
	//create config
	Expect(k8sClient.Create(context.Background(), snrConfigTmp)).To(Succeed())
	// verify it's created
	Eventually(func(g Gomega) {
		g.Expect(k8sClient.Get(context.Background(), client.ObjectKeyFromObject(snrConfig), snrConfigTmp)).To(Succeed())
	}, 10*time.Second, 100*time.Millisecond).Should(Succeed())

}
