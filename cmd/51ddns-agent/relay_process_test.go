package main

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"
)

func TestRelayExitPreservesHealthyProcess(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("requires Unix process signals; also exercised in the Linux snapshot guest")
	}
	dir := t.TempDir()
	t.Setenv("QA_RELAY_STATE_DIR", dir)
	helper := filepath.Join(dir, "fake-frpc")
	script := `#!/bin/sh
trap 'exit 0' INT TERM
case "$2" in *node-hk1*) node=node-hk1 ;; *) node=node-hk2 ;; esac
printf '%s %s\n' "$node" "$$" >> "$QA_RELAY_STATE_DIR/starts"
while :; do
 if [ "$node" = node-hk1 ] && [ -f "$QA_RELAY_STATE_DIR/fail-one" ]; then exit 23; fi
 sleep 1
done
`
	if err := os.WriteFile(helper, []byte(script), 0o700); err != nil {
		t.Fatal(err)
	}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodGet {
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{"relay_configs":[{"relay_node":"node-hk1","frpc_toml":"qa-one"},{"relay_node":"node-hk2","frpc_toml":"qa-two"}]}`))
			return
		}
		w.WriteHeader(http.StatusOK)
	}))
	defer server.Close()
	service := &agent{apiURL: server.URL, deviceID: "00000000-0000-4000-8000-000000000914",
		deviceToken: "qa-only", frpcPath: helper, configPath: filepath.Join(dir, "frpc.toml"),
		refresh: 50 * time.Millisecond, httpClient: server.Client()}
	ctx, cancel := context.WithCancel(context.Background())
	finished := make(chan error, 1)
	go func() { finished <- service.run(ctx) }()
	defer func() {
		cancel()
		select {
		case err := <-finished:
			if !errors.Is(err, context.Canceled) {
				t.Errorf("run stopped unexpectedly: %v", err)
			}
		case <-time.After(7 * time.Second):
			t.Error("manual stop did not terminate managed processes")
		}
	}()
	starts := func(node string) int {
		data, _ := os.ReadFile(filepath.Join(dir, "starts"))
		count := 0
		for _, line := range strings.Fields(string(data)) {
			if line == node {
				count++
			}
		}
		return count
	}
	await := func(condition func() bool) {
		t.Helper()
		deadline := time.Now().Add(12 * time.Second)
		for time.Now().Before(deadline) {
			if condition() {
				return
			}
			time.Sleep(20 * time.Millisecond)
		}
		t.Fatal("managed process condition not reached")
	}
	await(func() bool { return starts("node-hk1") == 1 && starts("node-hk2") == 1 })
	data, err := os.ReadFile(filepath.Join(dir, "starts"))
	if err != nil {
		t.Fatal(err)
	}
	var healthyPID int
	for _, line := range strings.Split(string(data), "\n") {
		fields := strings.Fields(line)
		if len(fields) == 2 && fields[0] == "node-hk2" {
			healthyPID, err = strconv.Atoi(fields[1])
			if err != nil {
				t.Fatal(err)
			}
		}
	}
	if healthyPID <= 0 {
		t.Fatal("healthy relay PID missing")
	}
	healthyProcess, err := os.FindProcess(healthyPID)
	if err != nil {
		t.Fatal(err)
	}
	defer healthyProcess.Release()
	if err := os.WriteFile(filepath.Join(dir, "fail-one"), nil, 0o600); err != nil {
		t.Fatal(err)
	}
	await(func() bool { return starts("node-hk1") >= 2 })
	time.Sleep(300 * time.Millisecond)
	if err := healthyProcess.Signal(syscall.Signal(0)); err != nil {
		t.Fatalf("healthy relay process stopped after other relay exited: %v", err)
	}
	if count := starts("node-hk2"); count != 1 {
		t.Fatalf("healthy relay restarted %d times after other relay exited", count-1)
	}
}
