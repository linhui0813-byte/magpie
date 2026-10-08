package provider

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

// A configured Token Plan must remain visible even before console sign-in,
// so the Usage page can explain why its allowance could not be read.
func TestQwenPlanCardNeedsConsole(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	t.Setenv("BAILIAN_CONFIG_DIR", t.TempDir())
	forgetPlanQuotas()
	t.Cleanup(forgetPlanQuotas)
	p, _ := FromPreset("qwen-token-plan")
	p.Key = "test-model-key"
	if _, err := Add(p); err != nil {
		t.Fatal(err)
	}
	qs := PlanQuotas(context.Background())
	for _, q := range qs {
		if q.Provider == p.ID {
			if q.Error == "" || len(q.Windows) != 0 {
				t.Fatalf("an unsigned console must report unknown allowance: %+v", q)
			}
			return
		}
	}
	t.Fatal("Qwen Token Plan is missing from the Usage cards")
}

// The envelope and fractional values are from the official CLI's
// token-plan-usage.test.ts. Monthly fields are read by Bailian's current
// personal Token Plan console (bailian-tokenplan 0.0.117).
const qwenMonthlyReply = `{"data":{"DataV2":{"data":{"data":{"per1MonthPercentage":0.25,"per1MonthResetTime":1793000000000}}}}}`

func TestReadQwenPlan(t *testing.T) {
	for _, tc := range []struct {
		name, body string
		used       []float64
	}{
		{"monthly", qwenMonthlyReply, []float64{25}},
		{"rolling", `{"data":{"DataV2":{"data":{"data":{"per5HourPercentage":0.5,"per5HourResetTime":1786000000000,"per1WeekPercentage":0.25,"per1WeekResetTime":1786100000000}}}}}`, []float64{50, 25}},
		{"unused", `{"per1MonthPercentage":0}`, []float64{0}},
		{"spent", `{"per1MonthPercentage":1}`, []float64{100}},
		{"overspent", `{"per1MonthPercentage":1.2}`, []float64{100}},
		{"partial", `{"per5HourPercentage":null,"per1WeekPercentage":0.5}`, []float64{50}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			ws, err := readQwenPlan([]byte(tc.body))
			if err != nil || len(ws) != len(tc.used) {
				t.Fatalf("windows: %+v, %v", ws, err)
			}
			for i, used := range tc.used {
				if ws[i].Used != used || ws[i].Unlimited {
					t.Errorf("window %d: %+v, want %.0f%% used", i, ws[i], used)
				}
			}
			if tc.name == "monthly" && (ws[0].Name != "Month" || ws[0].Span != 0 || ws[0].ResetsAt == nil || ws[0].ResetsAt.UnixMilli() != 1793000000000) {
				t.Errorf("monthly reset: %+v", ws[0])
			}
			if tc.name == "rolling" && (ws[0].Span != 5*time.Hour || ws[1].Span != 7*24*time.Hour) {
				t.Errorf("rolling spans: %+v", ws)
			}
			if tc.name == "unused" && ws[0].ResetsAt != nil {
				t.Errorf("invented reset: %+v", ws[0])
			}
		})
	}
	for _, body := range []string{
		`{}`, `null`, `{"per1MonthResetTime":1793000000000}`,
		`{"per1MonthPercentage":"not-a-number"}`, `{"per1MonthPercentage":-0.2}`,
		`{"per1MonthPercentage":0.2,"per1MonthResetTime":"wrong"}`,
		`{"per1MonthPercentage":0.2,"per1MonthResetTime":9223372036854775807}`,
		`{"data":{"success":false,"errorCode":"NoPermission","data":{"per1MonthPercentage":0}}}`,
		`{"data":{"DataV2":{"data":{"success":false,"data":{"per1MonthPercentage":0}}}}}`,
	} {
		if ws, err := readQwenPlan([]byte(body)); err == nil || len(ws) != 0 {
			t.Errorf("a missing or refused reading became a quota: %s => %+v, %v", body, ws, err)
		}
	}
	if _, err := readQwenPlan([]byte(`{"data":{"success":false,"errorCode":"NotLogined"}}`)); err != errQwenConsole {
		t.Errorf("expired session: %v", err)
	}
}

func TestQwenConsoleProfiles(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("BAILIAN_CONFIG_DIR", dir)
	for _, tc := range []struct {
		body, token, profile string
	}{
		{`{"access_token":"console-default","api_key":"model-key"}`, "console-default", "default"},
		{`{"active_config":"work","access_token":"other","work":{"access_token":"console-work","console_region":"cn-beijing","console_site":"domestic","console_switch_agent":123}}`, "console-work", "work"},
		{`{"active_config":"absent","access_token":"other"}`, "", ""},
		{`{"active_config":"work","access_token":"other","work":{"api_key":"model-key"}}`, "", ""},
		{`{"access_token":"other","console_site":"international"}`, "", ""},
		{`{"access_token":"other","console_region":"ap-southeast-1"}`, "", ""},
		{`{"access_token":`, "", ""},
	} {
		path := filepath.Join(dir, "config.json")
		if err := os.WriteFile(path, []byte(tc.body), 0o600); err != nil {
			t.Fatal(err)
		}
		c, err := readQwenConsole()
		if tc.token == "" {
			if err == nil {
				t.Error("invalid or unsigned profile accepted")
			}
		} else if err != nil || c.Token != tc.token || c.Profile != tc.profile {
			t.Errorf("wrong console profile selected: error=%v profile=%q", err, c.Profile)
		}
		b, _ := os.ReadFile(path)
		if string(b) != tc.body {
			t.Error("quota lookup changed the CLI configuration")
		}
	}
}

func TestQwenPlanCardAndRefresh(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	dir := t.TempDir()
	t.Setenv("BAILIAN_CONFIG_DIR", dir)
	if err := os.WriteFile(filepath.Join(dir, "config.json"), []byte(`{"access_token":"test-console-session","console_switch_agent":123}`), 0o600); err != nil {
		t.Fatal(err)
	}
	forgetPlanQuotas()
	t.Cleanup(forgetPlanQuotas)
	var calls atomic.Int32
	var status atomic.Int32
	status.Store(200)
	var session atomic.Value
	session.Store("test-console-session")
	var response atomic.Value
	response.Store(qwenMonthlyReply)
	transport := qwenTestTransport(func(r *http.Request) (*http.Response, error) {
		calls.Add(1)
		if r.Method != "POST" || r.URL.Host != "bailian-cs.console.aliyun.com" || r.URL.Path != "/cli/api.json" || r.Header.Get("Authorization") != "Bearer "+session.Load().(string) || r.URL.Query().Get("api") != qwenUsageAPI {
			t.Error("quota request did not use the official console gateway and session")
		}
		if err := r.ParseForm(); err != nil {
			t.Error(err)
		}
		var params struct {
			API  string `json:"Api"`
			Data struct {
				Corner struct {
					Protocol string `json:"protocol"`
					Agent    int64  `json:"switchAgent"`
				} `json:"cornerstoneParam"`
			} `json:"Data"`
		}
		if json.Unmarshal([]byte(r.Form.Get("params")), &params) != nil || params.API != qwenUsageAPI || params.Data.Corner.Protocol != "V2" || params.Data.Corner.Agent != 123 || r.Form.Get("region") != "cn-beijing" {
			t.Error("console scope or gateway envelope was lost")
		}
		return &http.Response{StatusCode: int(status.Load()), Header: make(http.Header), Body: io.NopCloser(strings.NewReader(response.Load().(string))), Request: r}, nil
	})
	old := http.DefaultClient.Transport
	http.DefaultClient.Transport = transport
	t.Cleanup(func() { http.DefaultClient.Transport = old })
	p, _ := FromPreset("qwen-token-plan")
	p.Key = "test-model-key"
	if _, err := Add(p); err != nil {
		t.Fatal(err)
	}
	// Several keys share one console allowance; ordinary DashScope is not
	// a Token Plan, and a similarly named third-party host is not Qwen.
	for _, other := range []Provider{
		{ID: "qwen-second", Chat: p.Chat, Key: "another-model-key"},
		{ID: "qwen-api", Chat: "https://dashscope.aliyuncs.com/compatible-mode/v1", Key: "ordinary-key"},
		{ID: "lookalike", Chat: "https://token-plan.maas.qianwenaiapi.com.example.com/v1", Key: "unrelated"},
	} {
		if err := Save(other); err != nil {
			t.Fatal(err)
		}
	}
	qs := PlanQuotas(context.Background())
	if len(qs) != 1 || qs[0].Provider != p.ID || qs[0].User != "Bailian console" || qs[0].Error != "" || len(qs[0].Windows) != 1 || qs[0].Windows[0].Used != 25 {
		t.Fatalf("Qwen card: %+v", qs)
	}
	PlanQuotas(context.Background())
	if calls.Load() != 1 {
		t.Error("the minute's cache did not hold")
	}
	// A transient outage retains a dated reading of this console session.
	status.Store(503)
	RefreshUsage(context.Background(), p.ID, "Bailian console")
	qs = PlanQuotas(context.Background())
	if len(qs) != 1 || qs[0].AsOf == nil || qs[0].Error != "" || len(qs[0].Windows) != 1 || qs[0].Windows[0].Used != 25 {
		t.Fatalf("the same session's last reading was lost: %+v", qs)
	}
	// A new session must not inherit that old account's allowance.
	session.Store("test-console-new")
	if err := os.WriteFile(filepath.Join(dir, "config.json"), []byte(`{"access_token":"test-console-new","console_switch_agent":123}`), 0o600); err != nil {
		t.Fatal(err)
	}
	RefreshUsage(context.Background(), p.ID, "Bailian console")
	qs = PlanQuotas(context.Background())
	if len(qs) != 1 || qs[0].AsOf != nil || qs[0].Error == "" || len(qs[0].Windows) != 0 {
		t.Fatalf("a new session inherited another account's last reading: %+v", qs)
	}
	status.Store(200)
	response.Store(`{"data":{"success":false,"errorCode":"NotLogined"}}`)
	RefreshUsage(context.Background(), p.ID, "Bailian console")
	qs = PlanQuotas(context.Background())
	if len(qs) != 1 || !strings.Contains(qs[0].Error, "bl auth login --console") || len(qs[0].Windows) != 0 || qs[0].AsOf != nil {
		t.Fatalf("an expired sign-in must not show the last account as current: %+v", qs)
	}
	if calls.Load() != 4 {
		t.Error("card refresh did not bypass the cache")
	}
}

// A vendor fixture at the HTTP boundary, with no socket or live credential.
type qwenTestTransport func(*http.Request) (*http.Response, error)

func (f qwenTestTransport) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }
