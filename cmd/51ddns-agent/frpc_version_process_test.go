package main

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func init() {
	if len(os.Args) != 2 || os.Args[1] != "--version" || filepath.Base(os.Args[0]) != "version-probe.exe" {
		return
	}
	switch os.Getenv("FRPC_VERSION_QA_MODE") {
	case "timeout":
		time.Sleep(30 * time.Second)
	case "oversized":
		fmt.Print(strings.Repeat("x", 4096))
	case "failure":
		fmt.Fprintln(os.Stderr, "synthetic-private-detail")
		os.Exit(9)
	default:
		os.Exit(8)
	}
	os.Exit(0)
}

func TestFRPCVersionProcessLimits(t *testing.T) {
	executable, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	probe := filepath.Join(t.TempDir(), "version-probe.exe")
	body, err := os.ReadFile(executable)
	if err != nil {
		t.Fatal(err)
	}
	if err = os.WriteFile(probe, body, 0700); err != nil {
		t.Fatal(err)
	}
	for _, mode := range []string{"timeout", "oversized", "failure", "cancelled"} {
		t.Run(mode, func(t *testing.T) {
			t.Setenv("FRPC_VERSION_QA_MODE", mode)
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			if mode == "cancelled" {
				cancel()
			}
			started := time.Now()
			version, err := detectFRPCVersion(ctx, probe)
			if err == nil || version != "" {
				t.Fatal("invalid version process accepted")
			}
			if strings.Contains(err.Error(), "synthetic-private-detail") {
				t.Fatal("stderr exposed")
			}
			if time.Since(started) > 6*time.Second {
				t.Fatal("bounded detection exceeded deadline")
			}
		})
	}
}
