package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"
)

func verifyIdentityCommand(args []string, input io.Reader) error {
	flags := flag.NewFlagSet("verify-identity", flag.ContinueOnError)
	flags.SetOutput(io.Discard)
	deviceID := flags.String("device-id", "", "")
	controlURL := flags.String("control-api-url", "https://api.51ddns.com", "")
	if err := flags.Parse(args); err != nil || flags.NArg() != 0 {
		return errors.New("invalid identity verification arguments")
	}
	tokenBytes, err := io.ReadAll(io.LimitReader(input, 4097))
	if err != nil || len(tokenBytes) > 4096 {
		return errors.New("account token could not be read")
	}
	token := strings.TrimSpace(string(tokenBytes))
	if token == "" || !deviceIDPattern.MatchString(*deviceID) {
		return errors.New("account token and valid device ID are required")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	return verifyDeviceIdentity(ctx, newControlHTTPClient(), strings.TrimRight(*controlURL, "/"), token, *deviceID)
}

func verifyDeviceIdentity(ctx context.Context, client *http.Client, controlURL, token, deviceID string) error {
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, controlURL+"/v1/agent/identity", nil)
	if err != nil {
		return err
	}
	request.Header.Set("Authorization", "Bearer "+token)
	request.Header.Set("X-51DDNS-Device-ID", deviceID)
	response, err := client.Do(request)
	if err != nil {
		return errors.New("identity endpoint unavailable")
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusNoContent {
		return fmt.Errorf("identity endpoint returned HTTP %d", response.StatusCode)
	}
	return nil
}
