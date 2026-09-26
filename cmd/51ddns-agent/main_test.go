package main

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestVersionIsInjectedAtBuildTime(t *testing.T) {
	previous := agentVersion
	agentVersion = "0.6.1"
	t.Cleanup(func() { agentVersion = previous })

	var output bytes.Buffer
	output.WriteString("51ddns-agent " + agentVersion + "\n")
	if output.String() != "51ddns-agent 0.6.1\n" {
		t.Fatalf("unexpected version output: %q", output.String())
	}
}

func TestFetchReturnsSortedRelayConfigurations(t *testing.T) {
	statusPath := filepath.Join(t.TempDir(), "status.json")
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer device-token" {
			t.Fatal("device authorization header was not sent")
		}
		if r.Header.Get("X-51DDNS-Device-ID") != "00000000-0000-4000-8000-000000000001" {
			t.Fatal("device id header was not sent")
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"frpc_toml":"legacy","plan":{"product_code":"free-7d","product_name":"免费体验","status":"active","starts_at":"2026-08-04T00:00:00Z","expires_at":"2026-08-11T00:00:00Z"},"relay_configs":[{"relay_node":"node-hk2","server_addr":"node2.example.com","frpc_toml":"second"},{"relay_node":"node-hk1","server_addr":"node1.example.com","frpc_toml":"first"}]}`))
	}))
	defer server.Close()

	service := &agent{apiURL: server.URL, deviceToken: "device-token", deviceID: "00000000-0000-4000-8000-000000000001", statusPath: statusPath, httpClient: server.Client()}
	items, err := service.fetch(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(items) != 2 || items[0].RelayNode != "node-hk1" || items[1].RelayNode != "node-hk2" {
		t.Fatalf("unexpected relay order: %#v", items)
	}
	var status localStatus
	content, err := os.ReadFile(statusPath)
	if err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(content, &status); err != nil {
		t.Fatal(err)
	}
	if !status.Online || status.Plan == nil || status.Plan.ProductCode != "free-7d" || status.Plan.ExpiresAt.Format("2006-01-02") != "2026-08-11" {
		t.Fatalf("unexpected local status: %#v", status)
	}
}

func TestFetchLimitsActiveRelaysForConstrainedDevices(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(`{"relay_configs":[{"relay_node":"node-hk2","frpc_toml":"second"},{"relay_node":"node-hk1","frpc_toml":"first"}]}`))
	}))
	defer server.Close()

	service := &agent{apiURL: server.URL, deviceToken: "device-token", maxActiveRelays: 1, httpClient: server.Client()}
	items, err := service.fetch(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(items) != 1 || items[0].RelayNode != "node-hk1" {
		t.Fatalf("unexpected constrained relay selection: %#v", items)
	}
}

func TestFetchAcceptsLegacyConfiguration(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(`{"frpc_toml":"legacy config"}`))
	}))
	defer server.Close()
	service := &agent{apiURL: server.URL, deviceToken: "device-token", httpClient: server.Client()}
	items, err := service.fetch(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(items) != 1 || items[0].RelayNode != "legacy" || items[0].FRPCTOML != "legacy config" {
		t.Fatalf("unexpected legacy normalization: %#v", items)
	}
}

func TestRelayConfigPath(t *testing.T) {
	service := &agent{configPath: filepath.Join("var", "lib", "51ddns", "frpc.toml")}
	if got := service.relayConfigPath("node-hk1", 1); got != service.configPath {
		t.Fatalf("single relay path = %q", got)
	}
	want := filepath.Join("var", "lib", "51ddns", "frpc.node-hk2.toml")
	if got := service.relayConfigPath("node-hk2", 2); got != want {
		t.Fatalf("multi relay path = %q, want %q", got, want)
	}
}

func TestActivateStoresAssignedDeviceID(t *testing.T) {
	const assigned = "00000000-0000-4000-8000-000000000009"
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/agent/activate" || r.Method != http.MethodPost {
			t.Fatalf("unexpected request: %s %s", r.Method, r.URL.Path)
		}
		if r.Header.Get("Authorization") != "Bearer account-token" {
			t.Fatal("account token was not sent")
		}
		var input map[string]string
		if err := json.NewDecoder(r.Body).Decode(&input); err != nil {
			t.Fatal(err)
		}
		if input["installation_id"] == "" || input["platform"] == "" || input["oem_voucher"] != "factory-voucher" {
			t.Fatalf("incomplete activation payload: %#v", input)
		}
		_ = json.NewEncoder(w).Encode(map[string]string{"device_id": assigned})
	}))
	defer server.Close()

	deviceFile := filepath.Join(t.TempDir(), "device.id")
	service := &agent{
		apiURL:         server.URL,
		deviceToken:    "account-token",
		deviceIDFile:   deviceFile,
		installationID: "00000000-0000-4000-8000-000000000008",
		oemVoucher:     "factory-voucher",
		httpClient:     server.Client(),
	}
	if err := service.activate(context.Background()); err != nil {
		t.Fatal(err)
	}
	content, err := os.ReadFile(deviceFile)
	if err != nil {
		t.Fatal(err)
	}
	if service.deviceID != assigned || string(content) != assigned+"\n" {
		t.Fatalf("assigned device id was not persisted: %q %q", service.deviceID, content)
	}
}

func TestActivateRecordsQuotaAndClearsItAfterSuccess(t *testing.T) {
	const assigned = "00000000-0000-4000-8000-000000000009"
	attempts := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		attempts++
		if attempts == 1 {
			w.WriteHeader(http.StatusConflict)
			_, _ = w.Write([]byte(`{"error":{"code":"device_quota_exceeded","message":"private response marker"}}`))
			return
		}
		_ = json.NewEncoder(w).Encode(map[string]string{"device_id": assigned})
	}))
	defer server.Close()

	directory := t.TempDir()
	service := &agent{
		apiURL: server.URL, deviceToken: "account-token",
		deviceIDFile: filepath.Join(directory, "device.id"),
		statusPath:   filepath.Join(directory, "status.json"), httpClient: server.Client(),
	}
	if err := service.activate(context.Background()); err == nil || err.Error() != "control plane activation rejected: device quota exceeded" {
		t.Fatalf("unexpected quota error: %v", err)
	}
	status := readTestLocalStatus(t, service.statusPath)
	if status.Online || status.ErrorCode != deviceQuotaErrorCode || status.UpdatedAt.IsZero() {
		t.Fatalf("unexpected quota status: %#v", status)
	}
	content, err := os.ReadFile(service.statusPath)
	if err != nil || bytes.Contains(content, []byte("private response marker")) {
		t.Fatalf("response details leaked into local status: %v", err)
	}
	if err := service.activate(context.Background()); err != nil {
		t.Fatal(err)
	}
	status = readTestLocalStatus(t, service.statusPath)
	if status.Online || status.ErrorCode != "" || status.DeviceID != assigned {
		t.Fatalf("quota status was not cleared: %#v", status)
	}
}

func TestActivateDoesNotReportQuotaForOtherFailures(t *testing.T) {
	for _, test := range []struct {
		name string
		code int
		body string
	}{
		{"other conflict", http.StatusConflict, `{"error":{"code":"invalid_token","message":"private response marker"}}`},
		{"quota code with wrong status", http.StatusInternalServerError, `{"error":{"code":"device_quota_exceeded","message":"private response marker"}}`},
		{"invalid response", http.StatusConflict, `not-json-private-response-marker`},
	} {
		t.Run(test.name, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				w.WriteHeader(test.code)
				_, _ = w.Write([]byte(test.body))
			}))
			defer server.Close()
			statusPath := filepath.Join(t.TempDir(), "status.json")
			service := &agent{apiURL: server.URL, deviceToken: "account-token", statusPath: statusPath, httpClient: server.Client()}
			err := service.activate(context.Background())
			if err == nil || !strings.Contains(err.Error(), "HTTP ") || strings.Contains(err.Error(), "private") {
				t.Fatalf("unexpected activation error: %v", err)
			}
			if status := readTestLocalStatus(t, statusPath); status.ErrorCode != "" || status.Online {
				t.Fatalf("unexpected local status: %#v", status)
			}
		})
	}
}

func TestActivateClearsQuotaAfterNetworkFailure(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {}))
	address := server.URL
	server.Close()
	service := &agent{
		apiURL: address, deviceToken: "account-token",
		statusPath: filepath.Join(t.TempDir(), "status.json"), httpClient: server.Client(),
	}
	service.recordActivationStatus(deviceQuotaErrorCode)
	if err := service.activate(context.Background()); err == nil {
		t.Fatal("expected network error")
	}
	if status := readTestLocalStatus(t, service.statusPath); status.ErrorCode != "" || status.Online {
		t.Fatalf("stale quota status was not cleared: %#v", status)
	}
}

func readTestLocalStatus(t *testing.T, path string) localStatus {
	t.Helper()
	content, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var status localStatus
	if err := json.Unmarshal(content, &status); err != nil {
		t.Fatal(err)
	}
	return status
}

func TestLoadOrCreateInstallationIDIsStable(t *testing.T) {
	path := filepath.Join(t.TempDir(), "install.id")
	first, err := loadOrCreateInstallationID(path)
	if err != nil {
		t.Fatal(err)
	}
	second, err := loadOrCreateInstallationID(path)
	if err != nil {
		t.Fatal(err)
	}
	if first != second || !deviceIDPattern.MatchString(first) {
		t.Fatalf("installation id is not stable: %q %q", first, second)
	}
}
