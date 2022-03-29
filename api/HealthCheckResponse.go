package api

type HealthCheckResponseCode int

const (
	RequestFailed HealthCheckResponseCode = -1
	Healthy       HealthCheckResponseCode = iota
	Unhealthy
	ApiError
)
