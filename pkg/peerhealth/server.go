package peerhealth

import (
	"context"
	"fmt"
	"net"
	"time"

	"github.com/go-logr/logr"
	"google.golang.org/grpc"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"sigs.k8s.io/controller-runtime/pkg/client"

	selfNodeRemediationApis "github.com/medik8s/self-node-remediation/api"
	"github.com/medik8s/self-node-remediation/api/v1alpha1"
	"github.com/medik8s/self-node-remediation/controllers"
	"github.com/medik8s/self-node-remediation/pkg/certificates"
)

const (
	connectionTimeout = 5 * time.Second
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
	c                client.Client
	reader           client.Reader
	log              logr.Logger
	certReader       certificates.CertStorageReader
	port             int
	apiServerTimeout time.Duration
}

// NewServer returns a new Server
func NewServer(c client.Client, reader client.Reader, log logr.Logger, port int, certReader certificates.CertStorageReader, apiServerTimeout time.Duration) (*Server, error) {
	return &Server{
		c:                c,
		reader:           reader,
		log:              log,
		certReader:       certReader,
		port:             port,
		apiServerTimeout: apiServerTimeout,
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
func (s *Server) IsHealthy(ctx context.Context, request *HealthRequest) (*HealthResponse, error) {
	s.log.Info("checking health for peer", "node", request.GetNodeName(), "machine", request.GetMachineName())

	nodeName := request.GetNodeName()
	if nodeName == "" {
		return nil, fmt.Errorf("empty node name in HealthRequest")
	}

	apiCtx, cancelFunc := context.WithTimeout(ctx, s.apiServerTimeout)
	defer cancelFunc()

	snrs := &v1alpha1.SelfNodeRemediationList{}
	if err := s.listWithTimeoutHandling(apiCtx, snrs); err != nil {
		s.log.Info("failed to list SelfNodeRemediations, returning ApiError", "cause", err.Error())
		return toResponse(selfNodeRemediationApis.ApiError)
	}

	// return healthy only if no snr matches that node
	for i := range snrs.Items {
		snrMatches, _, err := controllers.IsSNRMatching(ctx, s.c, &snrs.Items[i], nodeName, request.GetMachineName(), s.log)
		if err != nil {
			s.log.Error(err, "failed to check if SNR matches node")
			continue
		}
		if snrMatches {
			s.log.Info("found matching SNR, node is unhealthy", "node", nodeName, "machine", request.MachineName)
			return toResponse(selfNodeRemediationApis.Unhealthy)
		}
	}
	s.log.Info("no matching SNR found, node is considered healthy", "node", nodeName, "machine", request.MachineName)
	return toResponse(selfNodeRemediationApis.Healthy)
}

// listWithTimeoutHandling wraps a reader list method with additional context timeout handling.
// This is needed because we observed cases when the client code had some delay somewhere, which exceeded the
// configured timeout, and even the timeout on the caller side, causing unneeded fencing actions.
func (s *Server) listWithTimeoutHandling(apiCtx context.Context, snrs *v1alpha1.SelfNodeRemediationList) error {
	// run the list call async
	var listErr error
	listDone := make(chan struct{})
	go func() {
		// don't use cached client but the reader, because this also tests API server connectivity!
		listErr = s.reader.List(apiCtx, snrs)
		close(listDone)
	}()

	// wait until context expired or list call returned
	select {
	case <-apiCtx.Done():
		return fmt.Errorf("timed out")
	case <-listDone:
		return listErr
	}
}

func toResponse(status selfNodeRemediationApis.HealthCheckResponseCode) (*HealthResponse, error) {
	return &HealthResponse{
		Status: int32(status),
	}, nil
}
