package main

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestIdentityVerificationUsesCredentialAndExplicitID(t *testing.T) {
	const deviceID = "00000000-0000-4000-8000-000000000001"
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet || r.URL.Path != "/v1/agent/identity" {
			t.Errorf("unexpected verification request: %s %s", r.Method, r.URL.Path)
		}
		if r.Header.Get("Authorization") != "Bearer account-test-token" || r.Header.Get("X-51DDNS-Device-ID") != deviceID {
			t.Error("verification credential or device ID missing")
		}
		w.WriteHeader(http.StatusNoContent)
	}))
	defer server.Close()
	if err := verifyIdentityCommand([]string{"--device-id", deviceID, "--control-api-url", server.URL}, strings.NewReader("account-test-token")); err != nil {
		t.Fatal(err)
	}
}

func TestIdentityVerificationFailsClosedWithoutEchoingToken(t *testing.T) {
	const deviceID = "00000000-0000-4000-8000-000000000001"
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusUnauthorized)
	}))
	defer server.Close()
	err := verifyDeviceIdentity(context.Background(), server.Client(), server.URL, "secret-test-token", deviceID)
	if err == nil || !strings.Contains(err.Error(), "HTTP 401") || strings.Contains(err.Error(), "secret-test-token") {
		t.Fatalf("unexpected verification error: %v", err)
	}
}
