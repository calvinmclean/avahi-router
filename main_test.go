package main

import (
	"os"
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
			expected:       "api.local",
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
			if result != tt.expected {
				t.Errorf("getHostnameFromLabels() = %q, want %q", result, tt.expected)
			}
		})
	}

	// Clean up
	os.Unsetenv("TRAEFIK_ENABLED")
}
