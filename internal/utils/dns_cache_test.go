package utils

import (
	"errors"
	"testing"
)

func TestCachedCIDRResolverPreservesCacheOnFailure(t *testing.T) {
	calls := 0
	c := &CachedCIDRResolver{
		Resolver: func(string) ([]string, error) {
			calls++
			if calls == 1 {
				return []string{"203.0.113.5/32"}, nil
			}
			return nil, errors.New("simulated DNS failure")
		},
	}

	// first reconcile: resolver succeeds, cache is populated
	first, err := c.Resolve("endpoint.example.com")
	if err != nil {
		t.Fatalf("first resolve must succeed, got error: %v", err)
	}
	if len(first) == 0 {
		t.Fatal("first resolve must succeed")
	}

	// second reconcile: resolver fails for the same url - cached CIDRs must
	// be preserved
	second, err := c.Resolve("endpoint.example.com")
	if err == nil {
		t.Fatal("expected the simulated DNS failure to be returned")
	}
	if len(second) == 0 {
		t.Fatal("DNS recheck failure must preserve cached CIDRs — self-remediation risk without fix")
	}
	if second[0] != "203.0.113.5/32" {
		t.Fatalf("unexpected cached CIDR: %v", second)
	}
}

func TestCachedCIDRResolverFailsClosedOnFirstFailure(t *testing.T) {
	c := &CachedCIDRResolver{
		Resolver: func(string) ([]string, error) {
			return nil, errors.New("simulated DNS failure")
		},
	}

	cidrs, err := c.Resolve("endpoint.example.com")
	if err == nil {
		t.Fatal("expected the simulated DNS failure to be returned")
	}
	if len(cidrs) != 0 {
		t.Fatalf("expected no CIDRs on a first-ever resolution failure, got %v", cidrs)
	}
}
