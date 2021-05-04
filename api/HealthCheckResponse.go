package api

type HealthCheckResponse int

const (
	RequestFailed                     = -1
	Healthy       HealthCheckResponse = iota
	Unhealthy
	ApiError
)
