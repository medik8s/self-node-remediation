package api

type HealthCheckResponseCode int

const (
	// RequestFailed means the peer was dialled but did not answer within the request timeout: it is reachable, and
	// typically stalled on its own API server lookup. Synthesised on the client side.
	RequestFailed HealthCheckResponseCode = -1
	Healthy       HealthCheckResponseCode = iota
	Unhealthy
	ApiError
)

// PeerUnreachable means the peer could not be contacted at all, or the connection to it broke before it answered,
// so nothing is known about what it thinks. It is synthesised on the client side and never sent over the wire,
// which is why it lives outside the iota block above: inserting it there would renumber Healthy, Unhealthy and
// ApiError and change the gRPC contract.
//
// It is kept distinct from RequestFailed, which means the dial succeeded and the peer stayed silent until the
// deadline. The two say very different things about whether this node is isolated.
const PeerUnreachable HealthCheckResponseCode = -2
