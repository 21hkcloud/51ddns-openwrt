package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"testing"
	"time"
)

// The synthetic child uses no network and only writes inside the test directory.
// Ordinary test binaries cannot enter this path without its private config marker.
func init() {
	if len(os.Args) == 2 && os.Args[1] == "--version" && filepath.Base(os.Args[0]) == "synthetic-frpc.exe" {
		fmt.Println("0.70.0")
		os.Exit(0)
	}
	if len(os.Args) != 3 || os.Args[1] != "-c" {
		return
	}
	content, err := os.ReadFile(os.Args[2])
	if err != nil {
		return
	}
	var config retryChildConfig
	if json.Unmarshal(content, &config) != nil || config.Marker != "51ddns-relay-retry-synthetic-child-v1" {
		return
	}
	pid := os.Getpid()
	prefix := filepath.Join(config.Directory, fmt.Sprintf("%s-%d-%d", config.Node, config.Generation, pid))
	if err := os.WriteFile(prefix+".start", []byte(strconv.Itoa(pid)), 0o600); err != nil {
		os.Exit(91)
	}
	interrupt := make(chan os.Signal, 1)
	signal.Notify(interrupt, os.Interrupt, syscall.SIGTERM)
	ticker := time.NewTicker(10 * time.Millisecond)
	defer ticker.Stop()
	for {
		select {
		case <-interrupt:
			os.Exit(0)
		case <-ticker.C:
			_ = os.WriteFile(prefix+".heartbeat", []byte(strconv.FormatInt(time.Now().UnixNano(), 10)), 0o600)
			if _, err := os.Stat(filepath.Join(config.Directory, "exit-"+config.Node)); err == nil {
				os.Exit(23)
			}
		}
	}
}

type retryChildConfig struct {
	Marker     string
	Directory  string
	Node       string
	Generation int
}

type retryControl struct {
	t              *testing.T
	mu             sync.Mutex
	configurations []relayConfiguration
	reports        int
}

func (control *retryControl) RoundTrip(request *http.Request) (*http.Response, error) {
	if request.URL.Scheme != "http" || request.URL.Host != "127.0.0.1" {
		return nil, errors.New("synthetic transport rejected non-loopback URL")
	}
	if request.Header.Get("Authorization") != "Bearer retry-synthetic-token" || request.Header.Get("X-51DDNS-Device-ID") != "00000000-0000-4000-8000-000000000865" {
		return nil, errors.New("synthetic identity changed")
	}
	control.mu.Lock()
	defer control.mu.Unlock()
	var body []byte
	switch {
	case request.Method == http.MethodPost && request.URL.Path == "/v1/agent/ip-report":
		control.reports++
		body = []byte(`{}`)
	case request.Method == http.MethodGet && request.URL.Path == "/v1/agent/config":
		body, _ = json.Marshal(configuration{RelayConfigs: control.configurations})
	default:
		return nil, fmt.Errorf("unexpected synthetic request %s %s", request.Method, request.URL.Path)
	}
	return &http.Response{StatusCode: http.StatusOK, Header: http.Header{}, Body: io.NopCloser(bytes.NewReader(body)), Request: request}, nil
}

func (control *retryControl) reportCount() int {
	control.mu.Lock()
	defer control.mu.Unlock()
	return control.reports
}

func (control *retryControl) replace(configurations []relayConfiguration) {
	control.mu.Lock()
	defer control.mu.Unlock()
	control.configurations = configurations
}

type retryFixture struct {
	t          *testing.T
	directory  string
	state      string
	executable string
	alias      string
	service    *agent
	control    *retryControl
	cancel     context.CancelFunc
	finished   chan error
	stopped    bool
	identity   map[string][]byte
}

func newRetryFixture(t *testing.T, nodes ...string) *retryFixture {
	t.Helper()
	directory := t.TempDir()
	state := filepath.Join(directory, "children")
	if err := os.Mkdir(state, 0o700); err != nil {
		t.Fatal(err)
	}
	executable, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	fixture := &retryFixture{t: t, directory: directory, state: state, executable: executable,
		alias: filepath.Join(directory, "synthetic-frpc.exe"), finished: make(chan error, 1), identity: map[string][]byte{}}
	fixture.restoreExecutable()
	fixture.control = &retryControl{t: t, configurations: fixture.configurations(1, nodes...)}
	fixture.service = &agent{apiURL: "http://127.0.0.1", deviceToken: "retry-synthetic-token",
		deviceID: "00000000-0000-4000-8000-000000000865", installationID: "00000000-0000-4000-8000-000000000866",
		deviceIDFile: filepath.Join(directory, "device.id"), installationIDFile: filepath.Join(directory, "install.id"),
		frpcPath: fixture.alias, configPath: filepath.Join(directory, "config", "frpc.toml"),
		statusPath: filepath.Join(directory, "status.json"), refresh: 40 * time.Millisecond, retryBase: 40 * time.Millisecond,
		httpClient: &http.Client{Transport: fixture.control, Timeout: time.Second}}
	for path, content := range map[string]string{fixture.service.deviceIDFile: fixture.service.deviceID + "\n", fixture.service.installationIDFile: fixture.service.installationID + "\n"} {
		if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
			t.Fatal(err)
		}
		fixture.identity[path] = []byte(content)
	}
	t.Cleanup(fixture.stop)
	return fixture
}

func (fixture *retryFixture) configurations(generation int, nodes ...string) []relayConfiguration {
	output := make([]relayConfiguration, 0, len(nodes))
	for _, node := range nodes {
		body, _ := json.Marshal(retryChildConfig{Marker: "51ddns-relay-retry-synthetic-child-v1", Directory: fixture.state, Node: node, Generation: generation})
		output = append(output, relayConfiguration{RelayNode: node, FRPCTOML: string(body)})
	}
	return output
}

func (fixture *retryFixture) restoreExecutable() {
	fixture.t.Helper()
	if err := os.Symlink(fixture.executable, fixture.alias); err != nil {
		fixture.t.Fatalf("restore synthetic executable alias: %v", err)
	}
}

func (fixture *retryFixture) start() {
	ctx, cancel := context.WithCancel(context.Background())
	fixture.cancel = cancel
	go func() { fixture.finished <- fixture.service.run(ctx) }()
}

func (fixture *retryFixture) await(description string, condition func() bool) {
	fixture.t.Helper()
	// Windows graceful stop may consume five seconds, followed by the existing
	// three-second configuration replacement interval.
	deadline := time.Now().Add(12 * time.Second)
	for time.Now().Before(deadline) {
		select {
		case err := <-fixture.finished:
			fixture.stopped = true
			fixture.t.Fatalf("Agent exited before %s: %v", description, err)
		default:
		}
		if condition() {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	fixture.t.Fatalf("Agent did not reach %s", description)
}

func (fixture *retryFixture) children(node string, generation int) map[int]string {
	output := map[int]string{}
	entries, _ := filepath.Glob(filepath.Join(fixture.state, fmt.Sprintf("%s-%d-*.start", node, generation)))
	for _, path := range entries {
		content, err := os.ReadFile(path)
		if err != nil {
			continue
		}
		pid, err := strconv.Atoi(string(content))
		if err == nil {
			output[pid] = strings.TrimSuffix(path, ".start") + ".heartbeat"
		}
	}
	return output
}

func (fixture *retryFixture) firstChild(node string, generation int) (int, string) {
	for pid, heartbeat := range fixture.children(node, generation) {
		return pid, heartbeat
	}
	return 0, ""
}

func retryHeartbeat(path string) int64 {
	content, err := os.ReadFile(path)
	if err != nil {
		return 0
	}
	value, _ := strconv.ParseInt(string(content), 10, 64)
	return value
}

func (fixture *retryFixture) assertStatus(code string) {
	fixture.t.Helper()
	fixture.await("control-plane status "+code, func() bool {
		content, err := os.ReadFile(fixture.service.statusPath)
		if err != nil {
			return false
		}
		encodedDirectory, _ := json.Marshal(fixture.directory)
		if bytes.Contains(content, encodedDirectory[1:len(encodedDirectory)-1]) ||
			bytes.Contains(content, []byte("retry-synthetic-token")) ||
			bytes.Contains(content, []byte("51ddns-relay-retry-synthetic-child-v1")) {
			fixture.t.Fatal("local status exposed synthetic path or credential")
		}
		var status localStatus
		decoder := json.NewDecoder(bytes.NewReader(content))
		decoder.DisallowUnknownFields()
		return decoder.Decode(&status) == nil && status.Online && status.ErrorCode == code
	})
}

func (fixture *retryFixture) assertHealthy(node string, pid int, heartbeat string) {
	fixture.t.Helper()
	before := retryHeartbeat(heartbeat)
	fixture.await("healthy child heartbeat", func() bool { return retryHeartbeat(heartbeat) > before })
	children := fixture.children(node, 1)
	if len(children) != 1 || children[pid] != heartbeat {
		fixture.t.Fatalf("healthy relay was replaced: %#v", children)
	}
}

func (fixture *retryFixture) stop() {
	fixture.t.Helper()
	if fixture.cancel == nil {
		return
	}
	fixture.cancel()
	if !fixture.stopped {
		select {
		case err := <-fixture.finished:
			if !errors.Is(err, context.Canceled) {
				fixture.t.Errorf("manual stop returned %v", err)
			}
		case <-time.After(6500 * time.Millisecond):
			fixture.t.Error("manual stop failed to reap children within its bounded grace period")
		}
		fixture.stopped = true
	}
	heartbeats, _ := filepath.Glob(filepath.Join(fixture.state, "*.heartbeat"))
	before := map[string]int64{}
	for _, path := range heartbeats {
		before[path] = retryHeartbeat(path)
	}
	time.Sleep(100 * time.Millisecond)
	for path, value := range before {
		if retryHeartbeat(path) > value {
			fixture.t.Errorf("child remained live after run returned: %s", filepath.Base(path))
		}
	}
	for path, expected := range fixture.identity {
		actual, err := os.ReadFile(path)
		if err != nil || !bytes.Equal(actual, expected) {
			fixture.t.Errorf("relay recovery changed stored identity %s", filepath.Base(path))
		}
	}
	// Cleanup also covers a failing implementation, so a red test leaves no child.
	starts, _ := filepath.Glob(filepath.Join(fixture.state, "*.start"))
	for _, path := range starts {
		content, _ := os.ReadFile(path)
		pid, err := strconv.Atoi(string(content))
		if err == nil {
			if process, findErr := os.FindProcess(pid); findErr == nil {
				_ = process.Kill()
				_ = process.Release()
			}
		}
	}
}

func TestRelayRetryInitialConfigurationWriteFailure(t *testing.T) {
	fixture := newRetryFixture(t, "node-one")
	blocked := filepath.Dir(fixture.service.configPath)
	if err := os.WriteFile(blocked, []byte("synthetic-not-a-directory"), 0o600); err != nil {
		t.Fatal(err)
	}
	fixture.start()
	fixture.await("periodic reporting during initial write failure", func() bool { return fixture.control.reportCount() >= 3 })
	fixture.assertStatus("frpc_config_write_failed")
	if err := os.Remove(blocked); err != nil {
		t.Fatal(err)
	}
	fixture.await("automatic recovery after writable directory restored", func() bool { return len(fixture.children("node-one", 1)) == 1 })
	fixture.assertStatus("")
}

func TestRelayRetryInitialExecutableStartFailure(t *testing.T) {
	fixture := newRetryFixture(t, "node-one")
	if err := os.Remove(fixture.alias); err != nil {
		t.Fatal(err)
	}
	fixture.start()
	fixture.await("periodic reporting during missing executable", func() bool { return fixture.control.reportCount() >= 3 })
	fixture.assertStatus("frpc_start_failed")
	fixture.restoreExecutable()
	fixture.await("automatic recovery after executable restored", func() bool { return len(fixture.children("node-one", 1)) == 1 })
	fixture.assertStatus("")
}

func TestRelayRetryInitialFailurePreservesHealthyRelay(t *testing.T) {
	fixture := newRetryFixture(t, "node-bad", "node-good")
	blocked := fixture.service.relayConfigPath("node-bad", 2)
	if err := os.MkdirAll(blocked, 0o700); err != nil {
		t.Fatal(err)
	}
	fixture.start()
	fixture.await("healthy relay during initial peer failure", func() bool { return len(fixture.children("node-good", 1)) == 1 && fixture.control.reportCount() >= 3 })
	pid, heartbeat := fixture.firstChild("node-good", 1)
	fixture.assertStatus("frpc_config_write_failed")
	if err := os.Remove(blocked); err != nil {
		t.Fatal(err)
	}
	fixture.await("failed peer recovered", func() bool { return len(fixture.children("node-bad", 1)) == 1 })
	fixture.assertHealthy("node-good", pid, heartbeat)
	fixture.assertStatus("")
}

func TestRelayRetryExitedRelayFailurePreservesHealthyRelay(t *testing.T) {
	for _, failure := range []string{"write", "start"} {
		t.Run(failure, func(t *testing.T) {
			fixture := newRetryFixture(t, "node-bad", "node-good")
			fixture.start()
			fixture.await("both initial children", func() bool {
				return len(fixture.children("node-bad", 1)) == 1 && len(fixture.children("node-good", 1)) == 1
			})
			pid, heartbeat := fixture.firstChild("node-good", 1)
			blocked := fixture.service.relayConfigPath("node-bad", 2)
			code := "frpc_config_write_failed"
			if failure == "write" {
				if err := os.Remove(blocked); err != nil {
					t.Fatal(err)
				}
				if err := os.Mkdir(blocked, 0o700); err != nil {
					t.Fatal(err)
				}
			} else {
				code = "frpc_start_failed"
				if err := os.Remove(fixture.alias); err != nil {
					t.Fatal(err)
				}
			}
			exit := filepath.Join(fixture.state, "exit-node-bad")
			if err := os.WriteFile(exit, nil, 0o600); err != nil {
				t.Fatal(err)
			}
			reports := fixture.control.reportCount()
			fixture.await("periodic reports after peer exit", func() bool { return fixture.control.reportCount() >= reports+3 })
			fixture.assertStatus(code)
			fixture.assertHealthy("node-good", pid, heartbeat)
			if err := os.Remove(exit); err != nil {
				t.Fatal(err)
			}
			if failure == "write" {
				if err := os.Remove(blocked); err != nil {
					t.Fatal(err)
				}
			} else {
				fixture.restoreExecutable()
			}
			fixture.await("peer automatically restarted after fault removed", func() bool { return len(fixture.children("node-bad", 1)) == 2 })
			fixture.assertHealthy("node-good", pid, heartbeat)
			fixture.assertStatus("")
		})
	}
}

func TestRelayRetryConfigurationChangeAndManualStop(t *testing.T) {
	fixture := newRetryFixture(t, "node-one", "node-two")
	fixture.start()
	fixture.await("initial configuration", func() bool {
		return len(fixture.children("node-one", 1)) == 1 && len(fixture.children("node-two", 1)) == 1
	})
	_, firstHeartbeat := fixture.firstChild("node-one", 1)
	_, secondHeartbeat := fixture.firstChild("node-two", 1)
	fixture.control.replace(fixture.configurations(2, "node-one", "node-two"))
	fixture.await("updated configuration child group", func() bool {
		return len(fixture.children("node-one", 2)) == 1 && len(fixture.children("node-two", 2)) == 1
	})
	first, second := retryHeartbeat(firstHeartbeat), retryHeartbeat(secondHeartbeat)
	time.Sleep(100 * time.Millisecond)
	if retryHeartbeat(firstHeartbeat) != first || retryHeartbeat(secondHeartbeat) != second {
		t.Fatal("old configuration child remained live after replacement")
	}
	fixture.assertStatus("")
	fixture.stop()
}

func TestRelayRetryConfigurationRemovalDiscardsPendingRelay(t *testing.T) {
	fixture := newRetryFixture(t, "node-bad", "node-good")
	blocked := fixture.service.relayConfigPath("node-bad", 2)
	if err := os.MkdirAll(blocked, 0o700); err != nil {
		t.Fatal(err)
	}
	fixture.start()
	fixture.await("healthy node while old node is pending", func() bool { return len(fixture.children("node-good", 1)) == 1 && fixture.control.reportCount() >= 3 })
	fixture.control.replace(fixture.configurations(2, "node-good"))
	fixture.await("configuration that removed the pending node", func() bool { return len(fixture.children("node-good", 2)) == 1 })
	if err := os.Remove(blocked); err != nil {
		t.Fatal(err)
	}
	reports := fixture.control.reportCount()
	fixture.await("continued reporting after obsolete fault removed", func() bool { return fixture.control.reportCount() >= reports+4 })
	if len(fixture.children("node-bad", 1)) != 0 {
		t.Fatal("removed pending relay was resurrected by an obsolete retry")
	}
	fixture.assertStatus("")
}

func TestRelayRetryManualStopCancelsPendingBackoff(t *testing.T) {
	fixture := newRetryFixture(t, "node-one")
	if err := os.Remove(fixture.alias); err != nil {
		t.Fatal(err)
	}
	fixture.start()
	fixture.await("reporting while retry is pending", func() bool { return fixture.control.reportCount() >= 3 })
	before := time.Now()
	fixture.stop()
	if elapsed := time.Since(before); elapsed > 500*time.Millisecond {
		t.Fatalf("manual stop waited for retry backoff: %s", elapsed)
	}
}

func TestRelayRetryCrashLoopKeepsReporting(t *testing.T) {
	fixture := newRetryFixture(t, "node-one")
	if err := os.WriteFile(filepath.Join(fixture.state, "exit-node-one"), nil, 0o600); err != nil {
		t.Fatal(err)
	}
	fixture.start()
	fixture.await("control-plane reports during repeated child exits", func() bool {
		return len(fixture.children("node-one", 1)) >= 3 && fixture.control.reportCount() >= 5
	})
	fixture.assertStatus("frpc_process_exited")
}

func TestRelayRetryBackoffIsBounded(t *testing.T) {
	service := &agent{}
	for _, item := range []struct {
		failures int
		want     time.Duration
	}{{1, 3 * time.Second}, {2, 6 * time.Second}, {3, 12 * time.Second}, {4, 24 * time.Second}, {5, 30 * time.Second}, {1000, 30 * time.Second}} {
		if got := service.relayRetryDelay(item.failures); got != item.want {
			t.Errorf("retry delay after %d failures = %s, want %s", item.failures, got, item.want)
		}
	}
	service.retryBase = 40 * time.Millisecond
	if service.relayRetryDelay(1) != 40*time.Millisecond || service.relayRetryDelay(2) != 80*time.Millisecond || service.relayRetryDelay(1000) != 30*time.Second {
		t.Fatal("synthetic retry override changed the doubling or maximum")
	}
}

func TestRelayRetryExitTrackingRejectsStaleAndResetsAfterStableRun(t *testing.T) {
	now := time.Now()
	current := &managedProcess{relay: "node-one"}
	old := &managedProcess{relay: "node-one"}
	state := &relayRetry{configuration: relayConfiguration{RelayNode: "node-one"}, process: current,
		failures: 4, startedAt: now.Add(-time.Second)}
	if state.recordExit(processExit{relay: "node-one", process: old}, now, 30*time.Second) || state.process != current || state.failures != 4 {
		t.Fatal("old child's delayed exit changed current relay state")
	}
	if !state.recordExit(processExit{relay: "node-one", process: current}, now, 30*time.Second) || state.failures != 5 || state.process != nil || state.errorCode != "frpc_process_exited" {
		t.Fatal("short-lived child exit did not preserve growing backoff")
	}
	if state.recordExit(processExit{relay: "node-one", process: current}, now, 30*time.Second) {
		t.Fatal("the same child exit was processed twice")
	}
	state.process = current
	state.startedAt = now.Add(-31 * time.Second)
	if !state.recordExit(processExit{relay: "node-one", process: current}, now, 30*time.Second) || state.failures != 1 {
		t.Fatal("stable child run did not reset the previous crash-loop backoff")
	}
}

func TestRelayRetryDelayedExitPreservesCrashHistory(t *testing.T) {
	started := time.Now()
	process := &managedProcess{relay: "node-one"}
	state := &relayRetry{configuration: relayConfiguration{RelayNode: "node-one"}, process: process,
		failures: 4, startedAt: started}
	event := processExit{relay: "node-one", process: process, exitedAt: started.Add(time.Second)}
	if !state.recordExit(event, started.Add(45*time.Second), 30*time.Second) || state.failures != 5 {
		t.Fatal("late event consumption incorrectly treated a short child run as stable")
	}
}
