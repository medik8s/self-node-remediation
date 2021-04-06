package api

type HealthCheckResponse int

const (
	Healthy HealthCheckResponse = iota
	Unhealthy
	ApiError
)
