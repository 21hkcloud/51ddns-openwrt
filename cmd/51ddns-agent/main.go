package main

import (
	"bytes"
	"context"
	cryptorand "crypto/rand"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/http"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"reflect"
	"regexp"
	"runtime"
	"sort"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"
)

var agentVersion = "dev"

var relayNamePattern = regexp.MustCompile(`^[a-zA-Z0-9_-]{1,48}$`)
var deviceIDPattern = regexp.MustCompile(`^[0-9a-fA-F]{8}-[0-9a-fA-F]{4}-[1-5][0-9a-fA-F]{3}-[89abAB][0-9a-fA-F]{3}-[0-9a-fA-F]{12}$`)

type relayConfiguration struct {
	RelayNode  string `json:"relay_node"`
	ServerAddr string `json:"server_addr"`
	FRPCTOML   string `json:"frpc_toml"`
}

type configuration struct {
	FRPCTOML     string               `json:"frpc_toml"`
	RelayConfigs []relayConfiguration `json:"relay_configs"`
	Plan         *planStatus          `json:"plan,omitempty"`
}

type planStatus struct {
	ProductCode string    `json:"product_code"`
	ProductName string    `json:"product_name"`
	Status      string    `json:"status"`
	StartsAt    time.Time `json:"starts_at"`
	ExpiresAt   time.Time `json:"expires_at"`
}

type localStatus struct {
	DeviceID  string      `json:"device_id"`
	Online    bool        `json:"online"`
	Plan      *planStatus `json:"plan,omitempty"`
	UpdatedAt time.Time   `json:"updated_at"`
}

type managedProcess struct {
	relay string
	cmd   *exec.Cmd
	done  chan error
}

type processExit struct {
	relay string
	err   error
}

type agent struct {
	apiURL             string
	deviceToken        string
	deviceID           string
	deviceIDFile       string
	installationID     string
	installationIDFile string
	oemVoucher         string
	frpcPath           string
	configPath         string
	statusPath         string
	refresh            time.Duration
	maxActiveRelays    int
	httpClient         *http.Client
}

func main() {
	service, err := load()
	if err != nil {
		slog.Error("agent configuration rejected", "error", err)
		os.Exit(1)
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	if err := service.run(ctx); err != nil && !errors.Is(err, context.Canceled) {
		slog.Error("agent stopped", "error", err)
		os.Exit(1)
	}
}

func load() (*agent, error) {
	flags := flag.NewFlagSet("51ddns-agent", flag.ContinueOnError)
	tokenFile := flags.String("device-token-file", strings.TrimSpace(os.Getenv("DEVICE_TOKEN_FILE")), "read the device token from this file")
	deviceIDFile := flags.String("device-id-file", strings.TrimSpace(os.Getenv("DEVICE_ID_FILE")), "read the device id from this file")
	installationIDFile := flags.String("installation-id-file", value("INSTALLATION_ID_FILE", "/etc/51ddns/install.id"), "stable local installation id file")
	oemVoucherFile := flags.String("oem-voucher-file", strings.TrimSpace(os.Getenv("OEM_VOUCHER_FILE")), "optional factory-installed OEM voucher file")
	deviceIDFlag := flags.String("device-id", strings.TrimSpace(os.Getenv("DEVICE_ID")), "device id assigned by the control plane")
	controlURL := flags.String("control-api-url", value("CONTROL_API_URL", "https://api.51ddns.com"), "control-plane base URL")
	frpcPath := flags.String("frpc-path", value("FRPC_PATH", "frpc"), "path to the frpc executable")
	configPath := flags.String("config-path", value("FRPC_CONFIG_PATH", "/var/lib/51ddns/frpc.toml"), "path used for generated frpc configuration")
	statusPath := flags.String("status-path", value("STATUS_PATH", "/var/lib/51ddns/status.json"), "path used for non-sensitive local status")
	if err := flags.Parse(os.Args[1:]); err != nil {
		return nil, err
	}
	token := strings.TrimSpace(os.Getenv("DEVICE_TOKEN"))
	if token == "" && *tokenFile != "" {
		content, err := os.ReadFile(*tokenFile)
		if err != nil {
			return nil, fmt.Errorf("read DEVICE_TOKEN_FILE: %w", err)
		}
		token = strings.TrimSpace(string(content))
	}
	if token == "" {
		return nil, errors.New("DEVICE_TOKEN or --device-token-file is required")
	}
	deviceID := strings.TrimSpace(*deviceIDFlag)
	if deviceID == "" && *deviceIDFile != "" {
		content, err := os.ReadFile(*deviceIDFile)
		if err != nil {
			return nil, fmt.Errorf("read DEVICE_ID_FILE: %w", err)
		}
		deviceID = strings.TrimSpace(string(content))
	}
	if deviceID != "" && !deviceIDPattern.MatchString(deviceID) {
		return nil, errors.New("DEVICE_ID or --device-id must be a valid UUID")
	}
	installationID, err := loadOrCreateInstallationID(*installationIDFile)
	if err != nil {
		return nil, fmt.Errorf("load installation id: %w", err)
	}
	oemVoucher := ""
	if strings.TrimSpace(*oemVoucherFile) != "" {
		content, readErr := os.ReadFile(*oemVoucherFile)
		if readErr != nil && !errors.Is(readErr, os.ErrNotExist) {
			return nil, fmt.Errorf("read OEM voucher file: %w", readErr)
		}
		oemVoucher = strings.TrimSpace(string(content))
	}
	refresh := 30 * time.Second
	if raw := os.Getenv("CONFIG_REFRESH_SECONDS"); raw != "" {
		parsed, err := time.ParseDuration(raw + "s")
		if err != nil || parsed < 5*time.Second {
			return nil, errors.New("CONFIG_REFRESH_SECONDS must be at least 5")
		}
		refresh = parsed
	}
	maxActiveRelays := 0
	if raw := strings.TrimSpace(os.Getenv("MAX_ACTIVE_RELAYS")); raw != "" {
		parsed, err := strconv.Atoi(raw)
		if err != nil || parsed < 0 || parsed > 8 {
			return nil, errors.New("MAX_ACTIVE_RELAYS must be between 0 and 8")
		}
		maxActiveRelays = parsed
	}
	return &agent{
		apiURL:             strings.TrimRight(*controlURL, "/"),
		deviceToken:        token,
		deviceID:           deviceID,
		deviceIDFile:       *deviceIDFile,
		installationID:     installationID,
		installationIDFile: *installationIDFile,
		oemVoucher:         oemVoucher,
		frpcPath:           *frpcPath,
		configPath:         *configPath,
		statusPath:         *statusPath,
		refresh:            refresh,
		maxActiveRelays:    maxActiveRelays,
		httpClient:         &http.Client{Timeout: 15 * time.Second},
	}, nil
}

func (a *agent) run(ctx context.Context) error {
	var last []relayConfiguration
	for ctx.Err() == nil {
		if a.deviceID == "" {
			if err := a.activate(ctx); err != nil {
				slog.Warn("device activation failed", "error", err)
				if !wait(ctx, 10*time.Second) {
					break
				}
				continue
			}
		}
		if err := a.reportIP(ctx); err != nil {
			slog.Warn("public address report failed", "error", err)
		}
		current, err := a.fetch(ctx)
		if err != nil {
			slog.Warn("configuration fetch failed", "error", err)
			if !wait(ctx, 5*time.Second) {
				break
			}
			continue
		}
		processes, exits, err := a.startGroup(current)
		if err != nil {
			return err
		}
		last = cloneConfigurations(current)
		slog.Info("tunnel clients started", "relays", len(processes))

		ticker := time.NewTicker(a.refresh)
		restart := false
		for !restart {
			select {
			case <-ctx.Done():
				ticker.Stop()
				stopGroup(processes)
				return ctx.Err()
			case event := <-exits:
				ticker.Stop()
				slog.Warn("tunnel client exited", "relay", event.relay, "error", event.err)
				stopGroup(processes)
				restart = true
			case <-ticker.C:
				if err := a.reportIP(ctx); err != nil {
					slog.Warn("public address report failed", "error", err)
				}
				updated, err := a.fetch(ctx)
				if err != nil {
					slog.Warn("configuration refresh failed", "error", err)
					continue
				}
				if reflect.DeepEqual(updated, last) {
					continue
				}
				ticker.Stop()
				stopGroup(processes)
				slog.Info("relay or route configuration changed; restarting tunnel clients")
				restart = true
			}
		}
		if !wait(ctx, 3*time.Second) {
			break
		}
	}
	return ctx.Err()
}

func (a *agent) activate(ctx context.Context) error {
	payload, err := json.Marshal(map[string]string{
		"installation_id": a.installationID,
		"platform":        platformName(),
		"oem_voucher":     a.oemVoucher,
	})
	if err != nil {
		return err
	}
	request, err := http.NewRequestWithContext(ctx, http.MethodPost, a.apiURL+"/v1/agent/activate", bytes.NewReader(payload))
	if err != nil {
		return err
	}
	a.authorizeRequest(request)
	request.Header.Set("Content-Type", "application/json")
	response, err := a.httpClient.Do(request)
	if err != nil {
		return err
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(io.LimitReader(response.Body, 4096))
		return fmt.Errorf("control plane activation returned HTTP %d: %s", response.StatusCode, strings.TrimSpace(string(body)))
	}
	var result struct {
		DeviceID string `json:"device_id"`
	}
	if err := json.NewDecoder(io.LimitReader(response.Body, 64<<10)).Decode(&result); err != nil {
		return err
	}
	if !deviceIDPattern.MatchString(result.DeviceID) {
		return errors.New("control plane returned an invalid device id")
	}
	if strings.TrimSpace(a.deviceIDFile) == "" {
		return errors.New("--device-id-file is required for automatic activation")
	}
	if err := writePrivate(a.deviceIDFile, []byte(result.DeviceID+"\n")); err != nil {
		return fmt.Errorf("save assigned device id: %w", err)
	}
	a.deviceID = result.DeviceID
	slog.Info("device activated", "device_id", result.DeviceID)
	return nil
}

func platformName() string {
	if _, err := os.Stat("/etc/openwrt_release"); err == nil {
		return "openwrt"
	}
	if runtime.GOOS == "darwin" {
		return "macos"
	}
	return runtime.GOOS
}

func loadOrCreateInstallationID(path string) (string, error) {
	if content, err := os.ReadFile(path); err == nil {
		value := strings.TrimSpace(string(content))
		if deviceIDPattern.MatchString(value) {
			return value, nil
		}
	} else if !errors.Is(err, os.ErrNotExist) {
		return "", err
	}
	var raw [16]byte
	if _, err := io.ReadFull(cryptorand.Reader, raw[:]); err != nil {
		return "", err
	}
	raw[6] = (raw[6] & 0x0f) | 0x40
	raw[8] = (raw[8] & 0x3f) | 0x80
	value := fmt.Sprintf("%08x-%04x-%04x-%04x-%012x",
		raw[0:4], raw[4:6], raw[6:8], raw[8:10], raw[10:16])
	if err := writePrivate(path, []byte(value+"\n")); err != nil {
		return "", err
	}
	return value, nil
}

func (a *agent) startGroup(configurations []relayConfiguration) ([]*managedProcess, <-chan processExit, error) {
	exits := make(chan processExit, len(configurations))
	processes := make([]*managedProcess, 0, len(configurations))
	for _, configuration := range configurations {
		path := a.relayConfigPath(configuration.RelayNode, len(configurations))
		if err := writePrivate(path, []byte(configuration.FRPCTOML)); err != nil {
			stopGroup(processes)
			return nil, nil, fmt.Errorf("write %s frpc configuration: %w", configuration.RelayNode, err)
		}
		cmd := exec.Command(a.frpcPath, "-c", path)
		cmd.Stdout = os.Stdout
		cmd.Stderr = os.Stderr
		if err := cmd.Start(); err != nil {
			stopGroup(processes)
			return nil, nil, fmt.Errorf("start %s frpc: %w", configuration.RelayNode, err)
		}
		process := &managedProcess{relay: configuration.RelayNode, cmd: cmd, done: make(chan error, 1)}
		processes = append(processes, process)
		go func(item *managedProcess) {
			err := item.cmd.Wait()
			item.done <- err
			exits <- processExit{relay: item.relay, err: err}
		}(process)
	}
	return processes, exits, nil
}

func (a *agent) relayConfigPath(relay string, total int) string {
	if total == 1 {
		return a.configPath
	}
	extension := filepath.Ext(a.configPath)
	base := strings.TrimSuffix(a.configPath, extension)
	if extension == "" {
		extension = ".toml"
	}
	return base + "." + relay + extension
}

func (a *agent) reportIP(ctx context.Context) error {
	payload, err := json.Marshal(map[string]string{"ipv6": globalIPv6(), "client_version": agentVersion})
	if err != nil {
		return err
	}
	request, err := http.NewRequestWithContext(ctx, http.MethodPost, a.apiURL+"/v1/agent/ip-report", bytes.NewReader(payload))
	if err != nil {
		return err
	}
	a.authorizeRequest(request)
	request.Header.Set("Content-Type", "application/json")
	response, err := a.httpClient.Do(request)
	if err != nil {
		return err
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		_, _ = io.Copy(io.Discard, io.LimitReader(response.Body, 4096))
		return fmt.Errorf("control plane returned HTTP %d", response.StatusCode)
	}
	_, _ = io.Copy(io.Discard, io.LimitReader(response.Body, 4096))
	return nil
}

func globalIPv6() string {
	interfaces, err := net.Interfaces()
	if err != nil {
		return ""
	}
	for _, item := range interfaces {
		if item.Flags&net.FlagUp == 0 || item.Flags&net.FlagLoopback != 0 {
			continue
		}
		addresses, err := item.Addrs()
		if err != nil {
			continue
		}
		for _, address := range addresses {
			raw := strings.Split(address.String(), "/")[0]
			ip := net.ParseIP(raw)
			if ip != nil && ip.To4() == nil && ip.IsGlobalUnicast() && !ip.IsPrivate() {
				return ip.String()
			}
		}
	}
	return ""
}

func (a *agent) fetch(ctx context.Context) ([]relayConfiguration, error) {
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, a.apiURL+"/v1/agent/config", nil)
	if err != nil {
		return nil, err
	}
	a.authorizeRequest(request)
	response, err := a.httpClient.Do(request)
	if err != nil {
		return nil, err
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		_, _ = io.Copy(io.Discard, io.LimitReader(response.Body, 4096))
		return nil, fmt.Errorf("control plane returned HTTP %d", response.StatusCode)
	}
	var result configuration
	decoder := json.NewDecoder(io.LimitReader(response.Body, 2<<20))
	if err := decoder.Decode(&result); err != nil {
		return nil, err
	}
	if len(result.RelayConfigs) == 0 && strings.TrimSpace(result.FRPCTOML) != "" {
		result.RelayConfigs = []relayConfiguration{{RelayNode: "legacy", FRPCTOML: result.FRPCTOML}}
	}
	if len(result.RelayConfigs) == 0 || len(result.RelayConfigs) > 8 {
		return nil, errors.New("control plane returned an invalid relay configuration count")
	}
	seen := map[string]bool{}
	for index := range result.RelayConfigs {
		item := &result.RelayConfigs[index]
		item.RelayNode = strings.TrimSpace(item.RelayNode)
		item.ServerAddr = strings.TrimSpace(item.ServerAddr)
		if !relayNamePattern.MatchString(item.RelayNode) || strings.TrimSpace(item.FRPCTOML) == "" || seen[item.RelayNode] {
			return nil, errors.New("control plane returned an invalid relay configuration")
		}
		seen[item.RelayNode] = true
	}
	if err := a.writeLocalStatus(result.Plan); err != nil {
		slog.Warn("write local status failed", "error", err)
	}
	sort.Slice(result.RelayConfigs, func(i, j int) bool { return result.RelayConfigs[i].RelayNode < result.RelayConfigs[j].RelayNode })
	if a.maxActiveRelays > 0 && len(result.RelayConfigs) > a.maxActiveRelays {
		result.RelayConfigs = result.RelayConfigs[:a.maxActiveRelays]
		slog.Info("constrained relay mode enabled", "active_relays", len(result.RelayConfigs))
	}
	return result.RelayConfigs, nil
}

func (a *agent) writeLocalStatus(plan *planStatus) error {
	if strings.TrimSpace(a.statusPath) == "" {
		return nil
	}
	payload, err := json.Marshal(localStatus{
		DeviceID:  a.deviceID,
		Online:    true,
		Plan:      plan,
		UpdatedAt: time.Now().UTC(),
	})
	if err != nil {
		return err
	}
	payload = append(payload, '\n')
	return writePrivate(a.statusPath, payload)
}

func (a *agent) authorizeRequest(request *http.Request) {
	request.Header.Set("Authorization", "Bearer "+a.deviceToken)
	if a.deviceID != "" {
		request.Header.Set("X-51DDNS-Device-ID", a.deviceID)
	}
}

func cloneConfigurations(input []relayConfiguration) []relayConfiguration {
	output := make([]relayConfiguration, len(input))
	copy(output, input)
	return output
}

func writePrivate(path string, content []byte) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return err
	}
	temporary, err := os.CreateTemp(filepath.Dir(path), ".frpc-*.toml")
	if err != nil {
		return err
	}
	temporaryName := temporary.Name()
	defer os.Remove(temporaryName)
	if err := temporary.Chmod(0o600); err != nil {
		temporary.Close()
		return err
	}
	if _, err := temporary.Write(content); err != nil {
		temporary.Close()
		return err
	}
	if err := temporary.Sync(); err != nil {
		temporary.Close()
		return err
	}
	if err := temporary.Close(); err != nil {
		return err
	}
	return os.Rename(temporaryName, path)
}

func stopGroup(processes []*managedProcess) {
	var group sync.WaitGroup
	group.Add(len(processes))
	for _, process := range processes {
		go func(item *managedProcess) {
			defer group.Done()
			stopProcess(item.cmd, item.done)
		}(process)
	}
	group.Wait()
}

func stopProcess(cmd *exec.Cmd, done <-chan error) {
	if cmd.Process == nil {
		return
	}
	_ = cmd.Process.Signal(os.Interrupt)
	select {
	case <-done:
		return
	case <-time.After(5 * time.Second):
		_ = cmd.Process.Kill()
		<-done
	}
}

func wait(ctx context.Context, duration time.Duration) bool {
	timer := time.NewTimer(duration)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return false
	case <-timer.C:
		return true
	}
}

func value(key, fallback string) string {
	if current := strings.TrimSpace(os.Getenv(key)); current != "" {
		return current
	}
	return fallback
}
