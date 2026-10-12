package provider

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

const qwenExpiredReply = `{"data":{"success":false,"errorCode":"BailianGateway.Login.NotLogined"}}`

func qwenRenewTest(t *testing.T, profile string) string {
	t.Helper()
	dir := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	t.Setenv("BAILIAN_CONFIG_DIR", dir)
	forgetPlanQuotas()
	t.Cleanup(forgetPlanQuotas)
	resetLast := func() {
		lastQuotas.Lock()
		lastQuotas.m, lastQuotas.loaded = nil, false
		lastQuotas.Unlock()
	}
	resetLast()
	t.Cleanup(resetLast)
	qwenRenewLast.key, qwenRenewLast.at, qwenRenewLast.err = "", time.Time{}, nil
	old := runQwenConsoleCLI
	t.Cleanup(func() { runQwenConsoleCLI = old })
	qwenWriteTestConsole(t, dir, profile, "expired-console")
	return dir
}

func qwenWriteTestConsole(t *testing.T, dir, profile, token string) {
	t.Helper()
	c := map[string]any{"access_token": token, "console_region": "cn-beijing", "console_site": "domestic", "console_switch_agent": 123,
		"access_key_id": "test-access-id", "access_key_secret": "test-access-secret", "api_key": "unchanged-model-key"}
	root := c
	if profile != "default" {
		root = map[string]any{"active_config": profile, "access_token": "other-console", "api_key": "other-model-key", profile: c}
	}
	b, _ := json.Marshal(root)
	tmp := filepath.Join(dir, "config.json.tmp")
	if err := os.WriteFile(tmp, b, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Rename(tmp, filepath.Join(dir, "config.json")); err != nil {
		t.Fatal(err)
	}
}

func qwenRenewTransport(t *testing.T, fn qwenTestTransport) {
	t.Helper()
	old := http.DefaultClient.Transport
	http.DefaultClient.Transport = fn
	t.Cleanup(func() { http.DefaultClient.Transport = old })
}

func qwenRenewReply(r *http.Request, status int, body string) *http.Response {
	return &http.Response{StatusCode: status, Header: make(http.Header), Body: io.NopCloser(strings.NewReader(body)), Request: r}
}

func TestQwenPlanRenewsExpiredConsole(t *testing.T) {
	for _, profile := range []string{"default", "work"} {
		for _, status := range []int{200, 401} {
			t.Run(profile+"/"+http.StatusText(status), func(t *testing.T) {
				dir := qwenRenewTest(t, profile)
				var calls atomic.Int32
				runQwenConsoleCLI = func(ctx context.Context, c qwenConsole) error {
					calls.Add(1)
					if c.Dir != dir || c.Profile != profile || !c.CanRenew || c.SwitchAgent != 123 {
						t.Error("renewal used another console profile or scope")
					}
					if end, ok := ctx.Deadline(); !ok || time.Until(end) > 8*time.Second {
						t.Error("renewal was not bounded")
					}
					qwenWriteTestConsole(t, dir, profile, "renewed-console")
					return nil
				}
				qwenRenewTransport(t, func(r *http.Request) (*http.Response, error) {
					switch r.Header.Get("Authorization") {
					case "Bearer expired-console":
						return qwenRenewReply(r, status, qwenExpiredReply), nil
					case "Bearer renewed-console":
						return qwenRenewReply(r, 200, qwenMonthlyReply), nil
					default:
						t.Error("allowance was sent with another account or a model key")
						return qwenRenewReply(r, 401, ""), nil
					}
				})
				p, _ := FromPreset("qwen-token-plan")
				p.Key = "test-model-key"
				if _, err := Add(p); err != nil {
					t.Fatal(err)
				}
				qs := PlanQuotas(context.Background())
				if len(qs) != 1 || qs[0].Error != "" || qs[0].AsOf != nil || len(qs[0].Windows) != 1 || qs[0].Windows[0].Used != 25 {
					t.Fatalf("expired console was not renewed into a fresh allowance: %+v", qs)
				}
				if calls.Load() != 1 {
					t.Fatalf("renewed %d times, want once", calls.Load())
				}
				b, _ := os.ReadFile(filepath.Join(dir, "config.json"))
				if !strings.Contains(string(b), "unchanged-model-key") || profile == "work" && !strings.Contains(string(b), "other-model-key") {
					t.Error("renewal changed model credentials or another profile")
				}
			})
		}
	}
}

func TestQwenConsoleRenewsOnlyExpiredSessions(t *testing.T) {
	for _, tc := range []struct {
		name, body string
		status     int
		stripKeys  bool
	}{
		{"valid", qwenMonthlyReply, 200, false},
		{"permission", `{"data":{"success":false,"errorCode":"NoPermission"}}`, 200, false},
		{"outage", "", 503, false},
		{"malformed", "not json", 200, false},
		{"browser-only", qwenExpiredReply, 200, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			dir := qwenRenewTest(t, "default")
			if tc.stripKeys {
				if err := os.WriteFile(filepath.Join(dir, "config.json"), []byte(`{"access_token":"expired-console"}`), 0o600); err != nil {
					t.Fatal(err)
				}
			}
			runQwenConsoleCLI = func(context.Context, qwenConsole) error {
				t.Error("renewal ran without an expired session and stored AccessKey pair")
				return nil
			}
			qwenRenewTransport(t, func(r *http.Request) (*http.Response, error) { return qwenRenewReply(r, tc.status, tc.body), nil })
			c, err := readQwenConsole()
			if err != nil {
				t.Fatal(err)
			}
			_, ws, err := qwenConsoleAllowance(context.Background(), c)
			if tc.name == "valid" && (err != nil || len(ws) != 1) || tc.name != "valid" && (err == nil || len(ws) != 0) {
				t.Fatalf("allowance failure was misclassified: windows=%d error=%v", len(ws), err)
			}
		})
	}
}

func TestQwenConsoleRenewalCredentials(t *testing.T) {
	dir := qwenRenewTest(t, "default")
	for _, tc := range []struct {
		body string
		want bool
	}{
		{`{"access_token":"session","access_key_id":"id","access_key_secret":"secret"}`, true},
		{`{"access_token":"session","access_key_id":"id"}`, false},
		{`{"access_token":"session","access_key_id":"id","access_key_secret":" "}`, false},
		{`{"access_token":"session","access_key_id":"id","access_key_secret":"secret","security_token":"temporary"}`, false},
		{`{"active_config":"work","access_key_id":"other-id","access_key_secret":"other-secret","work":{"access_token":"session"}}`, false},
		{`{"active_config":"work","work":{"access_token":"session","access_key_id":"id","access_key_secret":"secret"}}`, true},
	} {
		if err := os.WriteFile(filepath.Join(dir, "config.json"), []byte(tc.body), 0o600); err != nil {
			t.Fatal(err)
		}
		c, err := readQwenConsole()
		if err != nil || c.CanRenew != tc.want {
			t.Errorf("wrong renewal eligibility: want=%v got=%v error=%v", tc.want, c.CanRenew, err)
		}
	}
}

func TestQwenConsoleRenewWaitHonorsCancellation(t *testing.T) {
	qwenRenewTest(t, "default")
	runQwenConsoleCLI = func(context.Context, qwenConsole) error {
		t.Error("a canceled reader started renewal")
		return nil
	}
	qwenRenewTransport(t, func(r *http.Request) (*http.Response, error) { return qwenRenewReply(r, 200, qwenExpiredReply), nil })
	c, _ := readQwenConsole()
	qwenRenewGate <- struct{}{}
	defer func() { <-qwenRenewGate }()
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	_, ws, err := qwenConsoleAllowance(ctx, c)
	if !errors.Is(err, errQwenConsole) || len(ws) != 0 {
		t.Fatalf("a canceled reader lost its expired-session error: %v", err)
	}
}

func TestQwenConsoleRenewFailureIsBoundedAndPrivate(t *testing.T) {
	qwenRenewTest(t, "default")
	var calls atomic.Int32
	runQwenConsoleCLI = func(context.Context, qwenConsole) error {
		calls.Add(1)
		return errors.New("test-access-secret and expired-console in CLI stderr")
	}
	qwenRenewTransport(t, func(r *http.Request) (*http.Response, error) { return qwenRenewReply(r, 200, qwenExpiredReply), nil })
	c, _ := readQwenConsole()
	for i := 0; i < 3; i++ {
		_, ws, err := qwenConsoleAllowance(context.Background(), c)
		if !errors.Is(err, errQwenConsole) || len(ws) != 0 || strings.Contains(err.Error(), "test-access-secret") || strings.Contains(err.Error(), "expired-console") {
			t.Fatalf("renewal failure exposed credentials or became an allowance: %v", err)
		}
	}
	if calls.Load() != 1 {
		t.Fatalf("failed renewal retried %d times within one minute", calls.Load())
	}
}

func TestQwenConsoleConcurrentRenewal(t *testing.T) {
	dir := qwenRenewTest(t, "default")
	var renews atomic.Int32
	runQwenConsoleCLI = func(context.Context, qwenConsole) error {
		renews.Add(1)
		qwenWriteTestConsole(t, dir, "default", "renewed-console")
		return nil
	}
	qwenRenewTransport(t, func(r *http.Request) (*http.Response, error) {
		if r.Header.Get("Authorization") == "Bearer expired-console" {
			return qwenRenewReply(r, 200, qwenExpiredReply), nil
		}
		return qwenRenewReply(r, 200, qwenMonthlyReply), nil
	})
	c, _ := readQwenConsole()
	var wg sync.WaitGroup
	for i := 0; i < 12; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			next, ws, err := qwenConsoleAllowance(context.Background(), c)
			if err != nil || len(ws) != 1 || next.Token != "renewed-console" {
				t.Error("a concurrent read did not use the renewed console session")
			}
		}()
	}
	wg.Wait()
	if renews.Load() != 1 {
		t.Fatalf("concurrent reads renewed %d times", renews.Load())
	}
}

func TestQwenConsoleDoesNotCrossProfileDuringRenewal(t *testing.T) {
	dir := qwenRenewTest(t, "default")
	runQwenConsoleCLI = func(context.Context, qwenConsole) error {
		qwenWriteTestConsole(t, dir, "work", "another-console")
		return nil
	}
	qwenRenewTransport(t, func(r *http.Request) (*http.Response, error) {
		if r.Header.Get("Authorization") != "Bearer expired-console" {
			t.Error("renewal queried the account the user switched to")
		}
		return qwenRenewReply(r, 200, qwenExpiredReply), nil
	})
	c, _ := readQwenConsole()
	_, ws, err := qwenConsoleAllowance(context.Background(), c)
	if err == nil || len(ws) != 0 || !strings.Contains(err.Error(), "profile changed") {
		t.Fatalf("another profile's allowance was accepted: windows=%d error=%v", len(ws), err)
	}
}

func TestQwenConsoleRenewedSessionHasOwnLastReading(t *testing.T) {
	dir := qwenRenewTest(t, "default")
	var expired atomic.Bool
	runQwenConsoleCLI = func(context.Context, qwenConsole) error {
		qwenWriteTestConsole(t, dir, "default", "renewed-console")
		return nil
	}
	qwenRenewTransport(t, func(r *http.Request) (*http.Response, error) {
		if r.Header.Get("Authorization") == "Bearer renewed-console" {
			return qwenRenewReply(r, 503, ""), nil
		}
		if expired.Load() {
			return qwenRenewReply(r, 200, qwenExpiredReply), nil
		}
		return qwenRenewReply(r, 200, qwenMonthlyReply), nil
	})
	p, _ := FromPreset("qwen-token-plan")
	p.Key = "test-model-key"
	if _, err := Add(p); err != nil {
		t.Fatal(err)
	}
	PlanQuotas(context.Background())
	expired.Store(true)
	RefreshUsage(context.Background(), p.ID, "Bailian console")
	qs := PlanQuotas(context.Background())
	if len(qs) != 1 || qs[0].Error == "" || qs[0].AsOf != nil || len(qs[0].Windows) != 0 {
		t.Fatalf("a renewed session inherited the old session's reading: %+v", qs)
	}
}

func TestQwenConsoleCLIEnv(t *testing.T) {
	env := qwenConsoleCLIEnv([]string{"Path=old-bin", "BAILIAN_CONFIG_DIR=other-account", "DASHSCOPE_BASE_URL=https://other.example",
		"DASHSCOPE_VERBOSE=1", "DO_NOT_TRACK=0", "KEEP=present"}, "selected-config", "cli-bin")
	joined := strings.Join(env, "\n")
	for _, value := range []string{"BAILIAN_CONFIG_DIR=selected-config", "PATH=cli-bin" + string(os.PathListSeparator) + "old-bin", "DO_NOT_TRACK=1", "KEEP=present"} {
		if !strings.Contains(joined, value) {
			t.Errorf("CLI environment lost %s", value)
		}
	}
	if strings.Contains(joined, "other-account") || strings.Contains(joined, "other.example") || strings.Contains(joined, "DASHSCOPE_VERBOSE=1") {
		t.Error("CLI used another config, region or verbose output")
	}
}

func TestQwenConsoleCLIUsesSelectedProfile(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("requires a POSIX shell fixture; this is not a Windows runtime check")
	}
	dir := qwenRenewTest(t, "work")
	bin := t.TempDir()
	// The fixture behaves like a CLI, and records only non-secret arguments.
	script := "#!/bin/sh\n/usr/bin/printf '%s\\n' \"$@\" > \"$BAILIAN_CONFIG_DIR/args\"\n/usr/bin/printf '%s' \"$BAILIAN_CONFIG_DIR\" > \"$BAILIAN_CONFIG_DIR/dir\"\nprintf 'a secret in stdout'\nprintf 'a secret in stderr' >&2\n"
	if err := os.WriteFile(filepath.Join(bin, "bl"), []byte(script), 0o700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", bin)
	c, _ := readQwenConsole()
	if err := qwenConsoleCLI(context.Background(), c); err != nil {
		t.Fatal(err)
	}
	b, _ := os.ReadFile(filepath.Join(dir, "args"))
	args := strings.Split(strings.TrimSpace(string(b)), "\n")
	joined := strings.Join(args, " ")
	if !strings.Contains(joined, "console call --api "+qwenUsageAPI+" --data {} --config work") || !strings.Contains(joined, "--timeout 8 --output json --quiet") {
		t.Fatalf("CLI did not request this profile's read-only allowance: %s", joined)
	}
	if strings.Contains(joined, c.Token) || strings.Contains(joined, "test-access-secret") || strings.Contains(joined, "unchanged-model-key") {
		t.Error("a credential was passed in process arguments")
	}
	b, _ = os.ReadFile(filepath.Join(dir, "dir"))
	if string(b) != dir {
		t.Error("CLI did not use the selected config directory")
	}
}

func TestQwenConsoleCLINpmShim(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("requires a POSIX shell fixture; this is not a Windows runtime check")
	}
	dir := qwenRenewTest(t, "work")
	bin := t.TempDir()
	entry := filepath.Join(bin, "node_modules", "bailian-cli", "dist", "bailian.mjs")
	if err := os.MkdirAll(filepath.Dir(entry), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(entry, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	script := "#!/bin/sh\n/usr/bin/printf '%s\\n' \"$@\" > \"$BAILIAN_CONFIG_DIR/node-args\"\n"
	if err := os.WriteFile(filepath.Join(bin, "node"), []byte(script), 0o700); err != nil {
		t.Fatal(err)
	}
	old := qwenCLIExecutable
	qwenCLIExecutable = func() string { return filepath.Join(bin, "bl.cmd") }
	t.Cleanup(func() { qwenCLIExecutable = old })
	t.Setenv("PATH", bin)
	c, _ := readQwenConsole()
	if err := qwenConsoleCLI(context.Background(), c); err != nil {
		t.Fatal(err)
	}
	b, _ := os.ReadFile(filepath.Join(dir, "node-args"))
	if !strings.HasPrefix(string(b), entry+"\nconsole\ncall\n") || !strings.Contains(string(b), "--config\nwork\n") {
		t.Error("an npm .cmd shim did not run its package with node and the selected profile")
	}
}
