package main

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestVersionOutputIsBounded(t *testing.T) {
	var b frpcVersionOutput
	if _, err := b.Write([]byte(strings.Repeat("x", 129))); err == nil || len(b.data) != 0 {
		t.Fatal("unbounded output accepted")
	}
}

func TestExportActualAdapterFixture(t *testing.T) {
	input, output := os.Getenv("FRPC_QA_INPUT"), os.Getenv("FRPC_QA_OUTPUT")
	if input == "" || output == "" {
		t.Skip("explicit synthetic loopback fixture required")
	}
	raw, err := os.ReadFile(input)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(raw), `auth.token = "synthetic-frp-token-not-a-production-secret"`) {
		t.Fatal("synthetic fixture required")
	}
	version, err := detectFRPCVersion(context.Background(), os.Getenv("FRPC_QA_EXECUTABLE"))
	if err != nil {
		t.Fatal(err)
	}
	body, err := configurationForFRPVersion(string(raw), version)
	if err != nil {
		t.Fatal(err)
	}
	f, err := os.OpenFile(output, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0600)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = f.WriteString(body); err != nil {
		f.Close()
		t.Fatal(err)
	}
	if err = f.Close(); err != nil {
		t.Fatal(err)
	}
}

func TestVersionFailurePreservesConfiguration(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "frpc.toml")
	if err := os.WriteFile(path, []byte("existing-private-config"), 0600); err != nil {
		t.Fatal(err)
	}
	a := &agent{frpcPath: filepath.Join(dir, "absent"), configPath: path}
	_, err := a.startProcess(context.Background(), relayConfiguration{RelayNode: "hk1", FRPCTOML: legacyInput}, 1, make(chan processExit, 1))
	if err == nil {
		t.Fatal("missing executable accepted")
	}
	got, err := os.ReadFile(path)
	if err != nil || string(got) != "existing-private-config" {
		t.Fatal("failed detection changed configuration")
	}
}

func TestActualFRPCVersionAndRefusal(t *testing.T) {
	exe := os.Getenv("FRPC_QA_EXECUTABLE")
	expected := os.Getenv("FRPC_QA_VERSION")
	if exe == "" {
		t.Skip("requires explicit isolated official frpc executable")
	}
	version, err := detectFRPCVersion(context.Background(), exe)
	if err != nil || version != expected {
		t.Fatalf("version mismatch: %q %v", version, err)
	}
	if version != "0.51.3" {
		return
	}
	dir := t.TempDir()
	path := filepath.Join(dir, "frpc.toml")
	if err := os.WriteFile(path, []byte("preserved"), 0600); err != nil {
		t.Fatal(err)
	}
	a := &agent{frpcPath: exe, configPath: path}
	_, err = a.startProcess(context.Background(), relayConfiguration{RelayNode: "hk1", FRPCTOML: legacyInput + "unknown = true\n"}, 1, make(chan processExit, 1))
	if err == nil {
		t.Fatal("unsupported config launched")
	}
	raw, _ := os.ReadFile(path)
	if string(raw) != "preserved" {
		t.Fatal("refusal changed saved configuration")
	}
}

const legacyInput = `serverAddr = "127.0.0.1"
serverPort = 7000
user = "test-device"
auth.method = "token"
auth.token = "synthetic-shared"
transport.tls.enable = true
metadatas.device_token = "synthetic-device"
metadatas.device_id = "test-device"

[[proxies]]
name = "route-http"
type = "http"
localIP = "::1"
localPort = 80
subdomain = "home"

[[proxies]]
name = "route-tcp"
type = "tcp"
localIP = "127.0.0.1"
localPort = 22
remotePort = 22000
`

func TestLegacyFRPAdapter(t *testing.T) {
	output, err := legacyFRPConfiguration(legacyInput)
	if err != nil {
		t.Fatal(err)
	}
	for _, part := range []string{"[common]\nserver_addr = 127.0.0.1", "authentication_method = token", "tls_enable = true", "meta_device_token = synthetic-device", "[route-http]\ntype = http\nlocal_ip = ::1", "[route-tcp]\ntype = tcp", "remote_port = 22000"} {
		if !strings.Contains(output, part) {
			t.Fatalf("missing %q", part)
		}
	}
}

func TestFRPVersionSelection(t *testing.T) {
	for _, version := range []string{"0.52.0", "0.70.0"} {
		got, err := configurationForFRPVersion(legacyInput, version)
		if err != nil || got != legacyInput {
			t.Fatal("modern configuration changed")
		}
	}
	got, err := configurationForFRPVersion(legacyInput, "0.51.3")
	if err != nil || !strings.HasPrefix(got, "[common]\n") {
		t.Fatal("legacy selection failed")
	}
	for _, version := range []string{"", "0.34.3", "0.51.2", "0.51.3-custom", "0.70.0\n", "0.052.0", "1.0.0"} {
		got, err := configurationForFRPVersion(legacyInput, version)
		if err == nil || got != "" {
			t.Fatalf("unverified version accepted: %q", version)
		}
	}
}

func TestLegacyFRPAdapterRefusesLossAndInjection(t *testing.T) {
	tests := map[string]string{
		"unknown":         legacyInput + "unknown = true\n",
		"duplicate":       strings.Replace(legacyInput, "serverPort = 7000", "serverPort = 7000\nserverPort = 7001", 1),
		"tls":             strings.Replace(legacyInput, "enable = true", "enable = false", 1),
		"missing":         strings.Replace(legacyInput, "auth.token = \"synthetic-shared\"\n", "", 1),
		"newline":         strings.Replace(legacyInput, "synthetic-shared", `secret\n[common]`, 1),
		"template":        strings.Replace(legacyInput, "synthetic-shared", "{{.Envs.SECRET}}", 1),
		"interpolation":   strings.Replace(legacyInput, "synthetic-shared", "%(token)s", 1),
		"comment":         strings.Replace(legacyInput, "synthetic-shared", "secret;comment", 1),
		"section":         strings.Replace(legacyInput, "route-http", "common", 1),
		"route-duplicate": strings.Replace(legacyInput, "route-tcp", "route-http", 1),
		"port":            strings.Replace(legacyInput, "remotePort = 22000", "remotePort = 65536", 1),
		"cross-identity":  strings.Replace(legacyInput, "user = \"test-device\"", "user = \"other\"", 1),
		"type":            strings.Replace(legacyInput, "type = \"tcp\"", "type = \"udp\"", 1),
		"empty-proxy":     legacyInput + "\n[[proxies]]\n",
		"wrong-shape":     strings.Replace(legacyInput, "remotePort = 22000", "subdomain = \"unexpected\"", 1),
	}
	for name, input := range tests {
		t.Run(name, func(t *testing.T) {
			body, err := legacyFRPConfiguration(input)
			if err == nil || body != "" {
				t.Fatal("unsafe configuration accepted")
			}
			if strings.Contains(err.Error(), "secret") {
				t.Fatal("credential leaked")
			}
		})
	}
}
