package peerhealth

import (
	"context"
	"fmt"
	"net"
	"time"

	"github.com/go-logr/logr"
	"google.golang.org/grpc"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/client-go/dynamic"
	"k8s.io/client-go/rest"

	selfNodeRemediationApis "github.com/medik8s/self-node-remediation/api"
	"github.com/medik8s/self-node-remediation/api/v1alpha1"
	"github.com/medik8s/self-node-remediation/controllers"
	"github.com/medik8s/self-node-remediation/pkg/certificates"
)

const (
	connectionTimeout = 5 * time.Second
	//IMPORTANT! this MUST be less than PeerRequestTimeout in apicheck
	//The difference between them should allow some time for sending the request over the network
	//todo enforce this
	apiServerTimeout = 3 * time.Second
)

var (
	snrRes = schema.GroupVersionResource{
		Group:    v1alpha1.GroupVersion.Group,
		Version:  v1alpha1.GroupVersion.Version,
		Resource: "selfnoderemediations",
	}
	nodeRes = schema.GroupVersionResource{
		Group:    corev1.SchemeGroupVersion.Group,
		Version:  corev1.SchemeGroupVersion.Version,
		Resource: "nodes",
	}
)

type Server struct {
	UnimplementedPeerHealthServer
	client     dynamic.Interface
	snr        *controllers.SelfNodeRemediationReconciler
	log        logr.Logger
	certReader certificates.CertStorageReader
	port       int
}

// NewServer returns a new Server
func NewServer(snr *controllers.SelfNodeRemediationReconciler, conf *rest.Config, log logr.Logger, port int, certReader certificates.CertStorageReader) (*Server, error) {

	// create dynamic client
	c, err := dynamic.NewForConfig(conf)
	if err != nil {
		return nil, err
	}

	return &Server{
		client:     c,
		snr:        snr,
		log:        log,
		certReader: certReader,
		port:       port,
	}, nil
}

// Start implements Runnable for usage by manager
func (s *Server) Start(ctx context.Context) error {

	serverCreds, err := certificates.GetServerCredentialsFromCerts(s.certReader)
	if err != nil {
		s.log.Error(err, "failed to get server credentials")
		return err
	}

	lis, err := net.Listen("tcp", fmt.Sprintf(":%d", s.port))
	if err != nil {
		s.log.Error(err, "failed to listen")
		return err
	}

	opts := []grpc.ServerOption{
		grpc.ConnectionTimeout(connectionTimeout),
		grpc.Creds(serverCreds),
	}
	grpcServer := grpc.NewServer(opts...)
	RegisterPeerHealthServer(grpcServer, s)

	errChan := make(chan error)
	go func() {
		if err := grpcServer.Serve(lis); err != nil {
			errChan <- err
		}
	}()

	s.log.Info("peer health server started")

	select {
	case err := <-errChan:
		return err
	case <-ctx.Done():
		grpcServer.Stop()
	}
	return nil
}

// IsHealthy checks if the given node is healthy
func (s Server) IsHealthy(ctx context.Context, request *HealthRequest) (*HealthResponse, error) {
	s.log.Info("IsHealthy", "node", request.GetNodeName())

	nodeName := request.GetNodeName()
	if nodeName == "" {
		return nil, fmt.Errorf("empty node name in HealthRequest")
	}

	apiCtx, cancelFunc := context.WithTimeout(ctx, apiServerTimeout)
	defer cancelFunc()

	//fetch all snrs from all ns
	snrs := &v1alpha1.SelfNodeRemediationList{}
	if err := s.snr.List(apiCtx, snrs); err != nil {
		s.log.Error(err, "api error failed to fetch snrs")
		return toResponse(selfNodeRemediationApis.ApiError)
	}

	//return healthy only if all of snrs are considered healthy for that node
	for _, snr := range snrs.Items {
		isOwnedByNHC := controllers.IsOwnedByNHC(&snr)
		if isOwnedByNHC && snr.Name == nodeName {
			s.log.Info("IsHealthy OWNED by NHC unhealthy", "snr name", snr.Name, "node", nodeName)
			return toResponse(selfNodeRemediationApis.Unhealthy)

		} else if !isOwnedByNHC && snr.Name == request.MachineName {
			s.log.Info("IsHealthy NOT OWNED by NHC unhealthy", "snr name", snr.Name, "machine", request.MachineName)
			return toResponse(selfNodeRemediationApis.Unhealthy)
		}
		s.log.Info("IsHealthy continue", "snr name", snr.Name)
	}
	s.log.Info("IsHealthy IS indeed healthy", "node", nodeName, "machine", request.MachineName)
	return toResponse(selfNodeRemediationApis.Healthy)
}

func (s Server) getNode(ctx context.Context, nodeName string) (*unstructured.Unstructured, error) {
	apiCtx, cancelFunc := context.WithTimeout(ctx, apiServerTimeout)
	defer cancelFunc()

	node, err := s.client.Resource(nodeRes).Namespace("").Get(apiCtx, nodeName, metav1.GetOptions{})
	if err != nil {
		s.log.Error(err, "api error")
		return nil, err
	}
	return node, nil
}

func toResponse(status selfNodeRemediationApis.HealthCheckResponseCode) (*HealthResponse, error) {
	return &HealthResponse{
		Status: int32(status),
	}, nil
}
