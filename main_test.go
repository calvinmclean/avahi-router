package main

import (
	"os"
	"reflect"
	"testing"
)

func TestExtractHostnameFromTraefikRule(t *testing.T) {
	tests := []struct {
		name     string
		rule     string
		expected string
	}{
		{
			name:     "Simple Host with backticks",
			rule:     "Host(`twchart.server.local`)",
			expected: "twchart.server.local",
		},
		{
			name:     "Host with double quotes",
			rule:     "Host(\"example.com\")",
			expected: "example.com",
		},
		{
			name:     "Host with PathPrefix",
			rule:     "Host(`api.example.com`) && PathPrefix(`/v1`)",
			expected: "api.example.com",
		},
		{
			name:     "PathPrefix before Host",
			rule:     "PathPrefix(`/`) && Host(`myapp.local`)",
			expected: "myapp.local",
		},
		{
			name:     "Multiple hosts",
			rule:     "Host(`host1.local`) || Host(`host2.local`)",
			expected: "host1.local",
		},
		{
			name:     "Host with port",
			rule:     "Host(`app.local:8080`)",
			expected: "app.local:8080",
		},
		{
			name:     "No Host rule",
			rule:     "PathPrefix(`/api`)",
			expected: "",
		},
		{
			name:     "Empty string",
			rule:     "",
			expected: "",
		},
		{
			name:     "Host without closing backtick",
			rule:     "Host(`incomplete.example.com)",
			expected: "",
		},
		{
			name:     "Host without closing quote",
			rule:     "Host(\"incomplete.example.com)",
			expected: "",
		},
		{
			name:     "Host with empty value",
			rule:     "Host(``)",
			expected: "",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := extractHostnameFromTraefikRule(tt.rule)
			if result != tt.expected {
				t.Errorf("extractHostnameFromTraefikRule(%q) = %q, want %q", tt.rule, result, tt.expected)
			}
		})
	}
}

func TestGetHostnameFromLabels(t *testing.T) {
	tests := []struct {
		name           string
		labels         map[string]string
		traefikEnabled bool
		expected       string
		expectedAny    []string
	}{
		{
			name:           "Standard avahi.hostname label",
			labels:         map[string]string{labelKey: "myhost.local"},
			traefikEnabled: false,
			expected:       "myhost.local",
		},
		{
			name:           "Standard label takes precedence over Traefik",
			labels:         map[string]string{labelKey: "avahi.local", "traefik.enable": "true", "traefik.http.routers.app.rule": "Host(`traefik.local`)"},
			traefikEnabled: true,
			expected:       "avahi.local",
		},
		{
			name:           "Traefik disabled - no avahi label",
			labels:         map[string]string{"traefik.enable": "true", "traefik.http.routers.app.rule": "Host(`traefik.local`)"},
			traefikEnabled: false,
			expected:       "",
		},
		{
			name:           "Traefik enabled with valid rule",
			labels:         map[string]string{"traefik.enable": "true", "traefik.http.routers.app.rule": "Host(`traefik.local`)"},
			traefikEnabled: true,
			expected:       "traefik.local",
		},
		{
			name:           "Traefik enabled but enable=false",
			labels:         map[string]string{"traefik.enable": "false", "traefik.http.routers.app.rule": "Host(`traefik.local`)"},
			traefikEnabled: true,
			expected:       "",
		},
		{
			name:           "Traefik enabled but no rule",
			labels:         map[string]string{"traefik.enable": "true"},
			traefikEnabled: true,
			expected:       "",
		},
		{
			name:           "Traefik enabled with multiple routers",
			labels:         map[string]string{"traefik.enable": "true", "traefik.http.routers.api.rule": "Host(`api.local`)", "traefik.http.routers.web.rule": "Host(`web.local`)"},
			traefikEnabled: true,
			expectedAny:    []string{"api.local", "web.local"},
		},
		{
			name:           "Traefik enabled with complex router name",
			labels:         map[string]string{"traefik.enable": "true", "traefik.http.routers.my-app-router.rule": "Host(`myapp.local`)"},
			traefikEnabled: true,
			expected:       "myapp.local",
		},
		{
			name:           "No relevant labels",
			labels:         map[string]string{"some.other.label": "value"},
			traefikEnabled: true,
			expected:       "",
		},
		{
			name:           "Empty labels",
			labels:         map[string]string{},
			traefikEnabled: true,
			expected:       "",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// Set environment variable
			if tt.traefikEnabled {
				os.Setenv("TRAEFIK_ENABLED", "true")
			} else {
				os.Unsetenv("TRAEFIK_ENABLED")
			}

			result := getHostnameFromLabels(tt.labels)
			if len(tt.expectedAny) > 0 {
				for _, expected := range tt.expectedAny {
					if result == expected {
						return
					}
				}
				t.Errorf("getHostnameFromLabels() = %q, want one of %v", result, tt.expectedAny)
				return
			}
			if result != tt.expected {
				t.Errorf("getHostnameFromLabels() = %q, want %q", result, tt.expected)
			}
		})
	}

	// Clean up
	os.Unsetenv("TRAEFIK_ENABLED")
}

func TestParseInterfaceNames(t *testing.T) {
	result := parseInterfaceNames(" eth0, wlan0 ,, eth0 ")
	expected := []string{"eth0", "wlan0"}
	if !reflect.DeepEqual(result, expected) {
		t.Fatalf("parseInterfaceNames() = %v, want %v", result, expected)
	}
}

func TestGetHostAddressesUsesHostIPFallback(t *testing.T) {
	t.Setenv("HOST_INTERFACES", "")
	t.Setenv("HOST_IP", "10.10.10.10")

	addresses, err := getHostAddresses()
	if err != nil {
		t.Fatalf("getHostAddresses() returned error: %v", err)
	}

	expected := []HostAddress{{Interface: "HOST_IP", IP: "10.10.10.10"}}
	if !reflect.DeepEqual(addresses, expected) {
		t.Fatalf("getHostAddresses() = %v, want %v", addresses, expected)
	}
}

func TestGetHostAddressesHOSTINTERFACESTakesPrecedence(t *testing.T) {
	t.Setenv("HOST_INTERFACES", "missing0")
	t.Setenv("HOST_IP", "10.10.10.10")

	_, err := getHostAddresses()
	if err == nil {
		t.Fatal("getHostAddresses() returned nil error, want HOST_INTERFACES precedence failure")
	}
	if want := `HOST_INTERFACES="missing0"`; err.Error() != want+" did not resolve to any usable IPv4 address" {
		t.Fatalf("getHostAddresses() error = %q, want %q", err.Error(), want+" did not resolve to any usable IPv4 address")
	}
}

func TestFormatHostAddresses(t *testing.T) {
	addresses := []HostAddress{
		{Interface: "eth0", IP: "192.168.1.100"},
		{Interface: "wlan0", IP: "10.0.0.5"},
	}

	got := formatHostAddresses(addresses)
	want := "eth0=192.168.1.100, wlan0=10.0.0.5"
	if got != want {
		t.Fatalf("formatHostAddresses() = %q, want %q", got, want)
	}
}
