package utils

import (
	"net"
	"sort"
)

// ResolveURLToSortedCIDRs resolves a hostname (or IP-literal) string to the
// /32 (IPv4) or /128 (IPv6) CIDRs of its current address(es).
func ResolveURLToSortedCIDRs(url string) ([]string, error) {
	if url == "" {
		return nil, nil
	}
	ips, err := net.LookupIP(url)
	if err != nil {
		return nil, err
	}
	return IpsToSortedCIDRs(ips), nil
}

// IpsToSortedCIDRs converts resolved IPs to /32 (IPv4) or /128 (IPv6) CIDRs,
// sorted lexically. This is to prevent returning the same address set
// in a different order on each lookup
func IpsToSortedCIDRs(ips []net.IP) []string {
	cidrs := make([]string, 0, len(ips))
	for _, ip := range ips {
		if ip4 := ip.To4(); ip4 != nil {
			cidrs = append(cidrs, ip4.String()+"/32")
		} else {
			cidrs = append(cidrs, ip.String()+"/128")
		}
	}
	sort.Strings(cidrs)
	return cidrs
}

// ResolveEndpointCIDRsWithFallback resolves url to sorted CIDRs via
// ResolveURLToSortedCIDRs. If that resolution fails, it falls back to
// cachedCIDRs instead of failing closed, but only if cachedURL equals url -
func ResolveEndpointCIDRsWithFallback(url, cachedURL string, cachedCIDRs []string) (cidrs []string, newCachedURL string, newCachedCIDRs []string, resolveErr error) {
	resolved, err := ResolveURLToSortedCIDRs(url)
	cidrs, newCachedURL, newCachedCIDRs = applyEndpointCIDRFallback(url, resolved, err, cachedURL, cachedCIDRs)
	return cidrs, newCachedURL, newCachedCIDRs, err
}

// applyEndpointCIDRFallback is the cache-fallback decision shared by
// ResolveEndpointCIDRsWithFallback and CachedCIDRResolver: on a successful
// resolution, cache it; on a failure, reuse the cache only if it was for
// this exact url
func applyEndpointCIDRFallback(url string, resolved []string, resolveErr error, cachedURL string, cachedCIDRs []string) (cidrs []string, newCachedURL string, newCachedCIDRs []string) {
	if resolveErr == nil {
		return resolved, url, resolved
	}
	if cachedURL == url {
		return cachedCIDRs, cachedURL, cachedCIDRs
	}
	return nil, cachedURL, cachedCIDRs
}

// CachedCIDRResolver resolves URLs to CIDRs via ResolveEndpointCIDRsWithFallback,
// managing the cache for the caller.
type CachedCIDRResolver struct {
	// Resolver overrides how a URL is resolved to CIDRs.
	Resolver func(string) ([]string, error)

	lastURL   string
	lastCIDRs []string
}

func (c *CachedCIDRResolver) Resolve(url string) ([]string, error) {
	resolve := c.Resolver
	if resolve == nil {
		resolve = ResolveURLToSortedCIDRs
	}
	resolved, err := resolve(url)
	cidrs, newCachedURL, newCachedCIDRs := applyEndpointCIDRFallback(url, resolved, err, c.lastURL, c.lastCIDRs)
	c.lastURL = newCachedURL
	c.lastCIDRs = newCachedCIDRs
	return cidrs, err
}
