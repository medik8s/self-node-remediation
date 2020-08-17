package apis

type HealthCheckResponse int

const (
	Healthy HealthCheckResponse = iota
	Unhealthy
	ApiError
)
