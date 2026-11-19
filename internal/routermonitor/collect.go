package routermonitor

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os/exec"
	"strconv"
	"strings"
	"time"
)

// Collector selects a fixed local probe or the existing SSH probe.
type Collector struct {
	RouterID string
	host     string
	local    bool
}

func NewCollector(mode, host, router, device string) (Collector, error) {
	switch mode {
	case "local":
		if host != "" || device == "" || (router != "" && router != device) {
			return Collector{}, errors.New("local router monitor must use its own device ID and cannot specify an SSH host")
		}
		return Collector{RouterID: device, local: true}, nil
	case "", "ssh":
		if host == "" && router == "" && mode == "" {
			return Collector{}, nil
		}
		if host == "" || strings.HasPrefix(host, "-") || router == "" {
			return Collector{}, errors.New("SSH router monitor requires a host alias and router ID")
		}
		return Collector{RouterID: router, host: host}, nil
	default:
		return Collector{}, errors.New("unknown router monitor mode")
	}
}

func (c Collector) command(ctx context.Context) *exec.Cmd {
	if c.local {
		return exec.CommandContext(ctx, "/bin/ash", "/data/router-guard/router-guard.sh", "status")
	}
	return exec.CommandContext(ctx, "ssh", "-o", "BatchMode=yes", "-o", "StrictHostKeyChecking=yes", "-o", "ClearAllForwardings=yes", "-o", "ForwardAgent=no", "-o", "ConnectTimeout=5", "--", c.host, "/bin/ash /data/router-guard/router-guard.sh status")
}

// Collect reads status using a locally provisioned SSH host alias.
func Collect(ctx context.Context, host string) Report {
	if host == "" || strings.HasPrefix(host, "-") {
		return Report{Failed: true}
	}
	return (Collector{host: host}).Collect(ctx)
}

func (c Collector) Collect(ctx context.Context) Report {
	ctx, cancel := context.WithTimeout(ctx, 8*time.Second)
	defer cancel()
	cmd := c.command(ctx)
	pipe, err := cmd.StdoutPipe()
	if err != nil || cmd.Start() != nil {
		return Report{Failed: true}
	}
	raw, err := io.ReadAll(io.LimitReader(pipe, 60001))
	_ = pipe.Close()
	if cmd.Wait() != nil || err != nil || len(raw) > 60000 {
		return Report{Failed: true}
	}
	if _, err = ParseSnapshot(bytes.NewReader(raw)); err != nil {
		return Report{Failed: true}
	}
	return Report{Raw: string(raw)}
}

func Publish(ctx context.Context, server, device, router, token string, report Report) error {
	raw, err := json.Marshal(report)
	if err != nil {
		return err
	}
	endpoint := strings.TrimRight(server, "/") + "/api/v1/devices/" + url.PathEscape(device) + "/router-monitor/" + url.PathEscape(router)
	req, err := http.NewRequestWithContext(ctx, http.MethodPut, endpoint, bytes.NewReader(raw))
	if err != nil {
		return err
	}
	if req.URL.Scheme != "https" {
		return errors.New("router monitor upload requires HTTPS")
	}
	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("Content-Type", "application/json")
	client := http.Client{Timeout: 10 * time.Second, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	res, err := client.Do(req)
	if err != nil {
		return err
	}
	defer res.Body.Close()
	if res.StatusCode != http.StatusNoContent {
		return errors.New("router monitor report rejected")
	}
	return nil
}

func (c Collector) eventsCommand(ctx context.Context, generation string, cursor int64, limit int) *exec.Cmd {
	genArg := generation
	if genArg == "" {
		genArg = "-"
	}
	cursorArg := strconv.FormatInt(cursor, 10)
	limitArg := strconv.Itoa(limit)
	if c.local {
		return exec.CommandContext(ctx, "/bin/ash", "/data/router-guard/router-guard.sh", "events", genArg, cursorArg, limitArg)
	}
	remoteCmd := fmt.Sprintf("/bin/ash /data/router-guard/router-guard.sh events %s %s %s", genArg, cursorArg, limitArg)
	return exec.CommandContext(ctx, "ssh", "-o", "BatchMode=yes", "-o", "StrictHostKeyChecking=yes", "-o", "ClearAllForwardings=yes", "-o", "ForwardAgent=no", "-o", "ConnectTimeout=5", "--", c.host, remoteCmd)
}

func (c Collector) CollectEvents(ctx context.Context, generation string, cursor int64, limit int) (EventBatch, error) {
	ctx, cancel := context.WithTimeout(ctx, 8*time.Second)
	defer cancel()
	cmd := c.eventsCommand(ctx, generation, cursor, limit)
	pipe, err := cmd.StdoutPipe()
	if err != nil || cmd.Start() != nil {
		return EventBatch{}, errors.New("failed to start events collector")
	}
	raw, err := io.ReadAll(io.LimitReader(pipe, 65536))
	_ = pipe.Close()
	if cmd.Wait() != nil || err != nil || len(raw) > 65535 {
		return EventBatch{}, errors.New("events command failed or exceeded size limit")
	}
	return ParseEvents(bytes.NewReader(raw))
}

func PublishEvents(ctx context.Context, server, device, router, token string, batch EventBatch) (int64, error) {
	raw, err := json.Marshal(batch)
	if err != nil {
		return 0, err
	}
	endpoint := strings.TrimRight(server, "/") + "/api/v1/devices/" + url.PathEscape(device) + "/router-monitor/" + url.PathEscape(router) + "/events"
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, bytes.NewReader(raw))
	if err != nil {
		return 0, err
	}
	if req.URL.Scheme != "https" {
		return 0, errors.New("router monitor upload requires HTTPS")
	}
	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("Content-Type", "application/json")
	client := http.Client{Timeout: 10 * time.Second, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	res, err := client.Do(req)
	if err != nil {
		return 0, err
	}
	defer res.Body.Close()
	if res.StatusCode != http.StatusOK {
		return 0, fmt.Errorf("events upload rejected with status %d", res.StatusCode)
	}
	var resp struct {
		AckCursor int64 `json:"ack_cursor"`
	}
	if err := json.NewDecoder(res.Body).Decode(&resp); err != nil {
		return 0, err
	}
	return resp.AckCursor, nil
}
