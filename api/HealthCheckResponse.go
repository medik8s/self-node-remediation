package api

type HealthCheckResponseCode int

const (
	RequestFailed HealthCheckResponseCode = -1
	Healthy       HealthCheckResponseCode = iota
	Unhealthy
	ApiError
)

// PeerUnreachable means the peer could not be dialled at all, so nothing is known
// about what it thinks. It is synthesised on the client side and never sent over
// the wire, which is why it lives outside the iota block above: inserting it there
// would renumber Healthy, Unhealthy and ApiError and change the gRPC contract.
//
// It is kept distinct from RequestFailed, which means the dial succeeded and the
// peer simply did not answer in time. The two say very different things about
// whether this node is isolated.
const PeerUnreachable HealthCheckResponseCode = -2
