package apicheck

import (
	"testing"
	"time"

	"github.com/go-logr/logr"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"k8s.io/client-go/tools/record"
	ctrl "sigs.k8s.io/controller-runtime"

	"github.com/medik8s/self-node-remediation/api/v1alpha1"
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

				expectedMinimumTimeout := config.ApiServerTimeout + v1alpha1.MinimumBuffer // 7s
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
