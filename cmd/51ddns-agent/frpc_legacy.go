package main

import (
	"context"
	"errors"
	"io"
	"os/exec"
	"strconv"
	"strings"
	"time"
)

type frpcVersionOutput struct{ data []byte }

func (b *frpcVersionOutput) Write(p []byte) (int, error) {
	if len(b.data)+len(p) > 128 {
		return 0, errors.New("frpc version output exceeds limit")
	}
	b.data = append(b.data, p...)
	return len(p), nil
}

func detectFRPCVersion(ctx context.Context, executable string) (string, error) {
	ctx, cancel := context.WithTimeout(ctx, 3*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, executable, "--version")
	var output frpcVersionOutput
	cmd.Stdout = &output
	cmd.Stderr = io.Discard
	cmd.WaitDelay = time.Second
	if err := cmd.Run(); err != nil {
		return "", errors.New("frpc version detection failed")
	}
	version := strings.TrimSpace(string(output.data))
	if version == "0.51.3" {
		return version, nil
	}
	if _, err := configurationForFRPVersion("", version); err != nil {
		return "", err
	}
	return version, nil
}

// The caller must supply the executable's verified version, never a UI label.
func configurationForFRPVersion(input, version string) (string, error) {
	if version == "0.51.3" {
		return legacyFRPConfiguration(input)
	}
	parts := strings.Split(version, ".")
	if len(parts) != 3 || parts[0] != "0" {
		return "", errors.New("unverified frpc version")
	}
	minor, err := strconv.Atoi(parts[1])
	patch, patchErr := strconv.Atoi(parts[2])
	if err != nil || patchErr != nil || minor < 52 || patch < 0 || strconv.Itoa(minor) != parts[1] || strconv.Itoa(patch) != parts[2] {
		return "", errors.New("unverified frpc version")
	}
	return input, nil
}

// This is an adapter for the control plane's emitted schema, not a TOML parser.
// Unknown syntax is refused rather than silently dropping security settings.
func legacyFRPConfiguration(input string) (string, error) {
	refused := errors.New("configuration cannot be represented safely for legacy frpc")
	common := map[string]string{"serverAddr": "server_addr", "serverPort": "server_port", "user": "user", "auth.method": "authentication_method", "auth.token": "token", "transport.tls.enable": "tls_enable", "metadatas.device_token": "meta_device_token", "metadatas.device_id": "meta_device_id"}
	proxy := map[string]string{"name": "name", "type": "type", "localIP": "local_ip", "localPort": "local_port", "subdomain": "subdomain", "remotePort": "remote_port"}
	sections := []map[string]string{{}}
	for _, line := range strings.Split(input, "\n") {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		if line == "[[proxies]]" {
			sections = append(sections, map[string]string{})
			continue
		}
		key, value, ok := strings.Cut(line, "=")
		key, value = strings.TrimSpace(key), strings.TrimSpace(value)
		mapping := common
		if len(sections) > 1 {
			mapping = proxy
		}
		if !ok || mapping[key] == "" {
			return "", refused
		}
		section := sections[len(sections)-1]
		if _, exists := section[key]; exists {
			return "", refused
		}
		switch key {
		case "serverPort", "localPort", "remotePort":
			port, err := strconv.Atoi(value)
			if err != nil || port < 1 || port > 65535 || strconv.Itoa(port) != value {
				return "", refused
			}
		case "transport.tls.enable":
			if value != "true" {
				return "", refused
			}
		default:
			if len(value) < 2 || value[0] != '"' || value[len(value)-1] != '"' {
				return "", refused
			}
			decoded, err := strconv.Unquote(value)
			if err != nil || !legacyFRPLiteral(decoded) {
				return "", refused
			}
			value = decoded
		}
		section[key] = value
	}
	for key := range common {
		if sections[0][key] == "" {
			return "", refused
		}
	}
	if sections[0]["auth.method"] != "token" || sections[0]["user"] != sections[0]["metadatas.device_id"] {
		return "", refused
	}
	var out strings.Builder
	out.WriteString("[common]\n")
	for _, key := range []string{"serverAddr", "serverPort", "user", "auth.method", "auth.token", "transport.tls.enable", "metadatas.device_token", "metadatas.device_id"} {
		out.WriteString(common[key] + " = " + sections[0][key] + "\n")
	}
	names := map[string]bool{"common": true}
	for _, s := range sections[1:] {
		for _, key := range []string{"name", "type", "localIP", "localPort"} {
			if s[key] == "" {
				return "", refused
			}
		}
		if names[s["name"]] {
			return "", refused
		}
		names[s["name"]] = true
		switch s["type"] {
		case "http", "https":
			if s["subdomain"] == "" || s["remotePort"] != "" {
				return "", refused
			}
		case "tcp":
			if s["remotePort"] == "" || s["subdomain"] != "" {
				return "", refused
			}
		default:
			return "", refused
		}
		out.WriteString("\n[" + s["name"] + "]\n")
		for _, key := range []string{"type", "localIP", "localPort", "subdomain", "remotePort"} {
			if s[key] != "" {
				out.WriteString(proxy[key] + " = " + s[key] + "\n")
			}
		}
	}
	return out.String(), nil
}

func legacyFRPLiteral(value string) bool {
	if value == "" {
		return false
	}
	for _, c := range value {
		if c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' || c >= '0' && c <= '9' || strings.ContainsRune("-._~:/@+=", c) {
			continue
		}
		return false
	}
	return true
}
