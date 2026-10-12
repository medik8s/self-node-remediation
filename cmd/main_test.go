package main

import (
	"strconv"
	"testing"
	"time"
)

func TestApiConnectivityCheckConfigFromEnv(t *testing.T) {
	for name, value := range map[string]string{
		"API_CHECK_INTERVAL":             "11000000000",
		"MAX_API_ERROR_THRESHOLD":        "5",
		"API_SERVER_TIMEOUT":             "7000000000",
		"PEER_DIAL_TIMEOUT":              "13000000000",
		"PEER_REQUEST_TIMEOUT":           "19000000000",
		"MAX_TIME_FOR_NO_PEERS_RESPONSE": "61000000000",
		"HOST_PORT":                      "30123",
	} {
		t.Setenv(name, value)
	}

	for _, minPeers := range []int{4, 0} {
		t.Run(strconv.Itoa(minPeers), func(t *testing.T) {
			t.Setenv("MIN_PEERS_FOR_REMEDIATION", strconv.Itoa(minPeers))
			config := getApiConnectivityCheckConfigFromEnv()

			for _, check := range []struct {
				name      string
				got, want any
			}{
				{"CheckInterval", config.CheckInterval, 11 * time.Second},
				{"MaxErrorsThreshold", config.MaxErrorsThreshold, 5},
				{"ApiServerTimeout", config.ApiServerTimeout, 7 * time.Second},
				{"PeerDialTimeout", config.PeerDialTimeout, 13 * time.Second},
				{"PeerRequestTimeout", config.PeerRequestTimeout, 19 * time.Second},
				{"MaxTimeForNoPeersResponse", config.MaxTimeForNoPeersResponse, 61 * time.Second},
				{"PeerHealthPort", config.PeerHealthPort, 30123},
				{"MinPeersForRemediation", config.MinPeersForRemediation, minPeers},
			} {
				if check.got != check.want {
					t.Errorf("%s = %v, want %v", check.name, check.got, check.want)
				}
			}
		})
	}
}
