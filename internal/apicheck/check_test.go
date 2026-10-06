package apicheck

import (
	"testing"
	"time"

	"github.com/go-logr/logr"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"k8s.io/client-go/tools/record"
	ctrl "sigs.k8s.io/controller-runtime"

	selfNodeRemediation "github.com/medik8s/self-node-remediation/api"
	snrwebhook "github.com/medik8s/self-node-remediation/internal/webhook/v1alpha1"
)

func TestApiCheck(t *testing.T) {
	RegisterFailHandler(Fail)
	RunSpecs(t, "ApiCheck Suite")
}

var _ = Describe("ApiConnectivityCheck", func() {
	var (
		apiCheck     *ApiConnectivityCheck
		config       *ApiConnectivityCheckConfig
		fakeRecorder *record.FakeRecorder
		log          logr.Logger
	)

	BeforeEach(func() {
		log = ctrl.Log.WithName("test")
		fakeRecorder = record.NewFakeRecorder(10)

		config = &ApiConnectivityCheckConfig{
			Log:                log,
			MyNodeName:         "test-node",
			ApiServerTimeout:   5 * time.Second,
			PeerRequestTimeout: 7 * time.Second,
			Recorder:           fakeRecorder,
		}

		apiCheck = &ApiConnectivityCheck{
			config: config,
		}
	})

	Describe("getEffectivePeerRequestTimeout", func() {
		Context("when PeerRequestTimeout is safe", func() {
			It("should return the configured PeerRequestTimeout", func() {
				// ApiServerTimeout=5s, PeerRequestTimeout=7s, MinimumBuffer=2s
				// 7s >= (5s + 2s), so it's safe
				effectiveTimeout := apiCheck.getEffectivePeerRequestTimeout()

				Expect(effectiveTimeout).To(Equal(7 * time.Second))

				// Should not emit any events
				Expect(len(fakeRecorder.Events)).To(Equal(0))
			})
		})

		Context("when PeerRequestTimeout is unsafe", func() {
			It("should return adjusted timeout and emit warning event", func() {
				config.PeerRequestTimeout = 6 * time.Second // Less than 5s + 2s = 7s

				effectiveTimeout := apiCheck.getEffectivePeerRequestTimeout()

				expectedMinimumTimeout := config.ApiServerTimeout + snrwebhook.MinimumBuffer // 7s
				Expect(effectiveTimeout).To(Equal(expectedMinimumTimeout))

				// Should emit a warning event
				Expect(len(fakeRecorder.Events)).To(Equal(1))
				event := <-fakeRecorder.Events
				Expect(event).To(ContainSubstring("Warning"))
				Expect(event).To(ContainSubstring("PeerTimeoutAdjusted"))
				Expect(event).To(ContainSubstring("6s")) // configured timeout
				Expect(event).To(ContainSubstring("5s")) // API server timeout
				Expect(event).To(ContainSubstring("7s")) // safe timeout
			})

		})

	})
})

var _ = Describe("sumPeersResponses", func() {
	var apiCheck *ApiConnectivityCheck

	BeforeEach(func() {
		apiCheck = &ApiConnectivityCheck{
			config: &ApiConnectivityCheckConfig{
				Log: ctrl.Log.WithName("test"),
			},
		}
	})

	drain := func(codes ...selfNodeRemediation.HealthCheckResponseCode) (int, int, int, int, int) {
		ch := make(chan selfNodeRemediation.HealthCheckResponseCode, len(codes))
		for _, c := range codes {
			ch <- c
		}
		return apiCheck.sumPeersResponses(len(codes), ch)
	}

	It("counts a peer that was dialled but did not answer separately from one that could not be dialled", func() {
		_, _, _, noAnswer, unreachable := drain(
			selfNodeRemediation.RequestFailed,
			selfNodeRemediation.PeerUnreachable,
			selfNodeRemediation.PeerUnreachable,
		)
		Expect(noAnswer).To(Equal(1), "RequestFailed means the dial succeeded and the peer stayed silent")
		Expect(unreachable).To(Equal(2), "PeerUnreachable means the dial itself failed")
	})

	It("still counts the answers the peers do give", func() {
		healthy, unhealthy, apiErrors, noAnswer, unreachable := drain(
			selfNodeRemediation.Healthy,
			selfNodeRemediation.Unhealthy,
			selfNodeRemediation.ApiError,
			selfNodeRemediation.ApiError,
			selfNodeRemediation.RequestFailed,
			selfNodeRemediation.PeerUnreachable,
		)
		Expect(healthy).To(Equal(1))
		Expect(unhealthy).To(Equal(1))
		Expect(apiErrors).To(Equal(2))
		Expect(noAnswer).To(Equal(1))
		Expect(unreachable).To(Equal(1))
	})
})

var _ = Describe("HealthCheckResponseCode wire values", func() {
	It("keeps the codes the peer sends over gRPC unchanged", func() {
		// PeerUnreachable is synthesised client side. If it were added to the iota
		// block it would renumber these and silently change the gRPC contract.
		Expect(int(selfNodeRemediation.Healthy)).To(Equal(1))
		Expect(int(selfNodeRemediation.Unhealthy)).To(Equal(2))
		Expect(int(selfNodeRemediation.ApiError)).To(Equal(3))
		Expect(int(selfNodeRemediation.RequestFailed)).To(Equal(-1))
		Expect(int(selfNodeRemediation.PeerUnreachable)).To(Equal(-2))
	})
})
