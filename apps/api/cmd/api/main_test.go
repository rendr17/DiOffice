package main

import "testing"

func TestValidateDevelopmentAuthBypassRequiresLoopbackAndHTTP(t *testing.T) {
	tests := []struct {
		name         string
		address      string
		webOrigin    string
		secureCookie bool
		wantError    bool
	}{
		{name: "loopback HTTP", address: "127.0.0.1:18080", webOrigin: "http://127.0.0.1:5174"},
		{name: "wildcard API bind", address: "0.0.0.0:18080", webOrigin: "http://127.0.0.1:5174", wantError: true},
		{name: "public API bind", address: "192.0.2.1:18080", webOrigin: "http://127.0.0.1:5174", wantError: true},
		{name: "hostname API bind", address: "localhost:18080", webOrigin: "http://127.0.0.1:5174", wantError: true},
		{name: "non-loopback web origin", address: "127.0.0.1:18080", webOrigin: "https://dioffice.example.com", wantError: true},
		{name: "secure cookies over local HTTP", address: "127.0.0.1:18080", webOrigin: "http://127.0.0.1:5174", secureCookie: true, wantError: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := validateDevelopmentAuthBypass(tt.address, tt.webOrigin, tt.secureCookie)
			if (err != nil) != tt.wantError {
				t.Fatalf("validateDevelopmentAuthBypass() error = %v, wantError %v", err, tt.wantError)
			}
		})
	}
}
