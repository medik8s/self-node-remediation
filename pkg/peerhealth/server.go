package peerhealth

import (
	"context"
	"fmt"
	"net"
	"time"

	"github.com/go-logr/logr"
	"google.golang.org/grpc"
	corev1 "k8s.io/api/core/v1"
	apiErrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/client-go/dynamic"
	"k8s.io/client-go/rest"
	"k8s.io/client-go/tools/cache"

	selfNodeRemediationApis "github.com/medik8s/self-node-remediation/api"
	"github.com/medik8s/self-node-remediation/api/v1alpha1"
	"github.com/medik8s/self-node-remediation/controllers"
	"github.com/medik8s/self-node-remediation/pkg/certificates"
)

const (
	connectionTimeout = 5 * time.Second
	machineAnnotation = "machine.openshift.io/machine" //todo this is openshift specific
	//IMPORTANT! this MUST be less than PeerRequestTimeout in apicheck
	//The difference between them should allow some time for sending the request over the network
	//todo enforce this
	apiServerTimeout = 3 * time.Second
)

var (
	pprRes = schema.GroupVersionResource{
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
	ppr        *controllers.SelfNodeRemediationReconciler
	log        logr.Logger
	certReader certificates.CertStorageReader
	port       int
}

// NewServer returns a new Server
func NewServer(ppr *controllers.SelfNodeRemediationReconciler, conf *rest.Config, log logr.Logger, port int, certReader certificates.CertStorageReader) (*Server, error) {

	// create dynamic client
	c, err := dynamic.NewForConfig(conf)
	if err != nil {
		return nil, err
	}

	return &Server{
		client:     c,
		ppr:        ppr,
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

	nodeName := request.GetNodeName()
	if nodeName == "" {
		return nil, fmt.Errorf("empty node name in HealthRequest")
	}

	s.log.Info("checking health for", "node", nodeName)

	namespace := s.ppr.GetLastSeenPprNamespace()
	isMachine := s.ppr.WasLastSeenPprMachine()

	// when namespace is empty, there wasn't a PPR yet, which also means that the node must be healthy
	if namespace == "" {
		// we didn't see a PPR yet, so the node is healthy
		// but we need to check for API error, so let's get node
		if _, err := s.getNode(ctx, nodeName); err != nil {
			// TODO do we need to deal with isNotFound, and if so, how?
			s.log.Info("no PPR seen yet, and API server issue, returning API error", "api error", err)
			return toResponse(selfNodeRemediationApis.ApiError)
		}
		s.log.Info("no PPR seen yet, node is healthy")
		return toResponse(selfNodeRemediationApis.Healthy)
	}

	if isMachine {
		return toResponse(s.isHealthyMachine(ctx, nodeName, namespace))
	} else {
		return toResponse(s.isHealthyNode(ctx, nodeName, namespace))
	}
}

func (s Server) isHealthyNode(ctx context.Context, nodeName string, namespace string) selfNodeRemediationApis.HealthCheckResponseCode {
	return s.isHealthyByPpr(ctx, nodeName, namespace)
}

func (s Server) isHealthyMachine(ctx context.Context, nodeName string, namespace string) selfNodeRemediationApis.HealthCheckResponseCode {
	node, err := s.getNode(ctx, nodeName)
	if err != nil {
		return selfNodeRemediationApis.ApiError
	}

	ann := node.GetAnnotations()
	namespacedMachine, exists := ann[machineAnnotation]

	if !exists {
		s.log.Info("node doesn't have machine annotation")
		return selfNodeRemediationApis.Unhealthy //todo is this the correct response?
	}
	_, machineName, err := cache.SplitMetaNamespaceKey(namespacedMachine)

	if err != nil {
		s.log.Error(err, "failed to parse machine annotation on the node")
		return selfNodeRemediationApis.Unhealthy //todo is this the correct response?
	}

	return s.isHealthyByPpr(ctx, machineName, namespace)
}

func (s Server) isHealthyByPpr(ctx context.Context, pprName string, pprNamespace string) selfNodeRemediationApis.HealthCheckResponseCode {
	apiCtx, cancelFunc := context.WithTimeout(ctx, apiServerTimeout)
	defer cancelFunc()

	_, err := s.client.Resource(pprRes).Namespace(pprNamespace).Get(apiCtx, pprName, metav1.GetOptions{})
	if err != nil {
		if apiErrors.IsNotFound(err) {
			s.log.Info("node is healthy")
			return selfNodeRemediationApis.Healthy
		}
		s.log.Error(err, "api error")
		return selfNodeRemediationApis.ApiError
	}

	s.log.Info("node is unhealthy")
	return selfNodeRemediationApis.Unhealthy
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
