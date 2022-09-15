package peerhealth

import (
	"context"
	"time"

	"github.com/go-logr/logr"
	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials"
)

type Client struct {
	PeerHealthClient
	conn *grpc.ClientConn
}

// NewClient return a new client for peer health checks. Don't forget to close it when done
func NewClient(serverAddr string, peerDialTimeout time.Duration, log logr.Logger, clientCreds credentials.TransportCredentials) (*Client, error) {

	var opts []grpc.DialOption

	if clientCreds != nil {
		opts = append(opts, grpc.WithTransportCredentials(clientCreds))
	} else {
		opts = append(opts, grpc.WithInsecure())
	}

	// this option implies WithBlock()
	opts = append(opts, grpc.WithReturnConnectionError())

	ctx, cancel := context.WithTimeout(context.Background(), peerDialTimeout)
	defer cancel()

	conn, err := grpc.DialContext(ctx, serverAddr, opts...)
	if err != nil {
		//TODO mshitrit uncomment
		//log.Error(err, "failed to dial")
		return nil, err
	}
	return &Client{
		PeerHealthClient: NewPeerHealthClient(conn),
		conn:             conn,
	}, nil
}

func (c *Client) Close() {
	c.conn.Close()
}
