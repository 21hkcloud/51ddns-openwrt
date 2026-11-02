package main

import (
	"context"
	"errors"
	"log/slog"
	"reflect"
	"time"
)

type relayStartError struct {
	code string
	err  error
}

func (e *relayStartError) Error() string { return e.code }
func (e *relayStartError) Unwrap() error { return e.err }

type relayRetry struct {
	configuration relayConfiguration
	process       *managedProcess
	failures      int
	retryAt       time.Time
	startedAt     time.Time
	errorCode     string
}

func (a *agent) relayRetryDelay(failures int) time.Duration {
	delay := a.retryBase
	if delay <= 0 {
		delay = 3 * time.Second
	}
	for i := 1; i < failures && delay < 30*time.Second; i++ {
		delay *= 2
	}
	if delay > 30*time.Second {
		delay = 30 * time.Second
	}
	return delay
}

func (r *relayRetry) recordExit(event processExit, now time.Time, stableFor time.Duration) bool {
	if r.process == nil || event.process != r.process || event.relay != r.configuration.RelayNode {
		return false
	}
	exitedAt := event.exitedAt
	if exitedAt.IsZero() {
		exitedAt = now
	}
	if exitedAt.Sub(r.startedAt) >= stableFor {
		r.failures = 0
	}
	r.process = nil
	r.failures++
	r.errorCode = "frpc_process_exited"
	return true
}

func (a *agent) recordRelayStatus(states []relayRetry) {
	code := ""
	for _, state := range states {
		if state.errorCode != "" {
			code = state.errorCode
			break
		}
	}
	if a.relayErrorCode == code && (code == "" || time.Since(a.lastLocalStatus.UpdatedAt) < 30*time.Second) {
		return
	}
	a.relayErrorCode = code
	status := a.lastLocalStatus
	status.ErrorCode = code
	if err := a.writeLocalStatus(status); err != nil {
		slog.Warn("write local status failed", "error", err)
	}
}

func (a *agent) runRelays(ctx context.Context, configurations []relayConfiguration) error {
	groupCtx, cancel := context.WithCancel(ctx)
	exits := make(chan processExit, len(configurations))
	states := make([]relayRetry, len(configurations))
	for i, configuration := range configurations {
		states[i].configuration = configuration
	}
	defer func() {
		cancel()
		processes := make([]*managedProcess, len(states))
		for i := range states {
			processes[i] = states[i].process
		}
		stopGroup(processes)
	}()

	startPending := func(now time.Time) {
		for i := range states {
			state := &states[i]
			if groupCtx.Err() != nil || state.process != nil || state.retryAt.After(now) {
				continue
			}
			process, err := a.startProcess(groupCtx, state.configuration, len(states), exits)
			attemptedAt := time.Now()
			if err != nil {
				if groupCtx.Err() != nil {
					return
				}
				state.failures++
				state.errorCode = "frpc_start_failed"
				var failure *relayStartError
				if errors.As(err, &failure) {
					state.errorCode = failure.code
				}
				state.retryAt = attemptedAt.Add(a.relayRetryDelay(state.failures))
				slog.Warn("tunnel client start failed; retry scheduled", "relay", state.configuration.RelayNode, "error_code", state.errorCode)
				continue
			}
			state.process = process
			state.startedAt = attemptedAt
			state.retryAt = time.Time{}
			state.errorCode = ""
			slog.Info("tunnel client started", "relay", state.configuration.RelayNode)
		}
		a.recordRelayStatus(states)
	}
	startPending(time.Now())
	ticker := time.NewTicker(a.refresh)
	defer ticker.Stop()
	timer := time.NewTimer(time.Hour)
	defer timer.Stop()
	for {
		if !timer.Stop() {
			select {
			case <-timer.C:
			default:
			}
		}
		var retry <-chan time.Time
		var next time.Time
		for _, state := range states {
			if state.process == nil && (next.IsZero() || state.retryAt.Before(next)) {
				next = state.retryAt
			}
		}
		if !next.IsZero() {
			timer.Reset(max(time.Until(next), 0))
			retry = timer.C
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-retry:
			startPending(time.Now())
		case event := <-exits:
			now := time.Now()
			for i := range states {
				state := &states[i]
				if !state.recordExit(event, now, 30*time.Second) {
					continue
				}
				state.retryAt = now.Add(a.relayRetryDelay(state.failures))
				slog.Warn("tunnel client exited; retry scheduled", "relay", state.configuration.RelayNode, "error_code", state.errorCode)
				break
			}
			a.recordRelayStatus(states)
		case <-ticker.C:
			if err := a.reportIP(ctx); err != nil {
				slog.Warn("public address report failed", "error", err)
			}
			updated, err := a.fetch(ctx)
			if err != nil {
				slog.Warn("configuration refresh failed", "error", err)
				continue
			}
			if !reflect.DeepEqual(updated, configurations) {
				slog.Info("relay or route configuration changed; restarting tunnel clients")
				return nil
			}
		}
	}
}
