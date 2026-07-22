package main

import "testing"

func TestInsecureTCPBindAllowed(t *testing.T) {
	tests := []struct {
		name          string
		address       string
		allowInsecure bool
		wantErr       bool
	}{
		{name: "loopback ipv4", address: "127.0.0.1:8888"},
		{name: "loopback ipv4 subnet", address: "127.0.0.5:8888"},
		{name: "loopback ipv6", address: "[::1]:8888"},
		{name: "localhost", address: "localhost:8888"},
		{name: "wildcard empty host", address: ":8888", wantErr: true},
		{name: "wildcard ipv4", address: "0.0.0.0:8888", wantErr: true},
		{name: "wildcard ipv6", address: "[::]:8888", wantErr: true},
		{name: "public ipv4", address: "203.0.113.5:8888", wantErr: true},
		{name: "public dns name", address: "panel.example.com:8888", wantErr: true},
		{name: "unparseable", address: "not-an-address", wantErr: true},
		{name: "wildcard allowed by opt-in", address: "0.0.0.0:8888", allowInsecure: true},
		{name: "public allowed by opt-in", address: "203.0.113.5:8888", allowInsecure: true},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			err := insecureTCPBindAllowed(test.address, test.allowInsecure)
			if (err != nil) != test.wantErr {
				t.Fatalf("insecureTCPBindAllowed(%q, %v) error = %v, wantErr %v", test.address, test.allowInsecure, err, test.wantErr)
			}
		})
	}
}
