package provider

import (
	"context"
	"encoding/json"
	"io"
	"math"
	"net/http"
	"strings"
	"testing"
	"time"
)

const geminiBillingReply = `{"jobComplete":true,"totalRows":"1","rows":[{"f":[{"v":"HKD"},{"v":"10.35"},{"v":"2026-10-14T05:00:00Z"},{"v":"2026-10-13T23:00:00Z"}]}]}`

func geminiBillingFixture() *GeminiBilling {
	credits := 160.0
	return &GeminiBilling{Table: "billing-project.billing.gcp_billing_export_v1_AAAAAA_BBBBBB_CCCCCC", Location: "US", BillingAccount: "AAAAAA-BBBBBB-CCCCCC", ServiceID: "ABCD-1234-5678", Currency: "HKD", Since: "2026-10-12", Credits: &credits}
}

func TestReadGeminiBillingEstimate(t *testing.T) {
	c := geminiBillingFixture()
	got := readGeminiBilling([]byte(geminiBillingReply), c)
	if got.err != nil || got.amount != "≈ HK$149.65" || got.estimate == nil || got.estimate.LatestUsage.Format(time.RFC3339) != "2026-10-13T23:00:00Z" {
		t.Fatalf("recorded funds less exported net charges, with data age: %+v", got)
	}
	for _, change := range []struct{ old, next string }{
		{`"jobComplete":true`, `"jobComplete":false`},
		{`"totalRows":"1"`, `"totalRows":"2"`},
		{`"HKD"`, `"USD"`},
		{`"10.35"`, `"NaN"`},
		{`"10.35"`, `null`},
		{`"2026-10-13T23:00:00Z"`, `"unknown"`},
	} {
		r := readGeminiBilling([]byte(strings.Replace(geminiBillingReply, change.old, change.next, 1)), c)
		if r.err == nil || r.amount != "" || r.estimate != nil {
			t.Fatalf("unknown or wrong-scope data became a balance: %s => %+v", change.next, r)
		}
	}
	if r := readGeminiBilling([]byte(`{"jobComplete":true,"totalRows":"0","rows":[]}`), c); r.err == nil || r.amount != "" {
		t.Fatalf("no exported usage must not be taken as zero spend: %+v", r)
	}
	for _, pair := range []struct{ cost, want string }{{"170", "≈ HK$-10.00"}, {"-5", "≈ HK$165.00"}, {"160", "≈ HK$0.00"}} {
		r := readGeminiBilling([]byte(strings.Replace(geminiBillingReply, "10.35", pair.cost, 1)), c)
		if r.err != nil || r.amount != pair.want {
			t.Fatalf("overrun/refund/zero: %+v, want %s", r, pair.want)
		}
	}
}

func TestGeminiBillingQueryScopeAndCache(t *testing.T) {
	isolate(t)
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	t.Cleanup(ForgetBalances)
	oldToken, oldClient := readGeminiBillingToken, http.DefaultClient
	t.Cleanup(func() { readGeminiBillingToken, http.DefaultClient = oldToken, oldClient })
	readGeminiBillingToken = func(context.Context) (string, error) { return "billing-only-token", nil }
	geminiBillingCache = map[string]geminiBillingReading{}
	calls := 0
	http.DefaultClient = &http.Client{Transport: volcengineBalanceTransport(func(req *http.Request) (*http.Response, error) {
		calls++
		if req.Method != "POST" || req.URL.String() != "https://bigquery.googleapis.com/bigquery/v2/projects/billing-project/queries" || req.Header.Get("Authorization") != "Bearer billing-only-token" || req.Header.Get("X-Goog-User-Project") != "billing-project" || req.Header.Get("X-Model-Only") != "" {
			t.Fatalf("billing request must use only its own credentials: %s %v", req.URL, req.Header)
		}
		b, _ := io.ReadAll(req.Body)
		var body struct {
			Query      string `json:"query"`
			Limit      string `json:"maximumBytesBilled"`
			Parameters []struct {
				Name  string
				Value struct{ Value string } `json:"parameterValue"`
			} `json:"queryParameters"`
		}
		if json.Unmarshal(b, &body) != nil || body.Limit != "1073741824" || !strings.Contains(body.Query, "billing_account_id = @account AND service.id = @service") || !strings.Contains(body.Query, "UNNEST(credits)") || strings.Contains(body.Query, "project.id") || strings.Contains(string(b), "model-key") {
			t.Fatalf("query must scope the shared account, net credits and cost cap: %s", b)
		}
		if len(body.Parameters) != 3 || body.Parameters[0].Value.Value != "AAAAAA-BBBBBB-CCCCCC" || body.Parameters[1].Value.Value != "ABCD-1234-5678" || body.Parameters[2].Value.Value != "2026-10-12" {
			t.Fatalf("wrong account, service or start date: %s", b)
		}
		return &http.Response{StatusCode: 200, Body: io.NopCloser(strings.NewReader(geminiBillingReply)), Header: http.Header{}}, nil
	})}
	p, _ := FromPreset("google")
	p.Key, p.GeminiBilling, p.Headers = "model-key", geminiBillingFixture(), map[string]string{"X-Model-Only": "private"}
	if err := Save(p); err != nil {
		t.Fatal(err)
	}
	second := p
	second.ID, second.Key = "google-second", "second-model-key"
	if err := Save(second); err != nil {
		t.Fatal(err)
	}
	qs := KeyBalances(context.Background())
	if len(qs) != 1 || qs[0].Balance != "≈ HK$149.65" || qs[0].BillingEstimate == nil || qs[0].ReadAt == nil || qs[0].BalanceTrend != nil || calls != 1 {
		t.Fatalf("one dated estimate for the shared account, without a wallet trend: %+v calls=%d", qs, calls)
	}
	firstRead := *qs[0].ReadAt
	ForgetBalances()
	qs = KeyBalances(context.Background())
	if calls != 1 || !qs[0].ReadAt.Equal(firstRead) {
		t.Fatalf("query cache fabricated a new reading: %+v calls=%d", qs, calls)
	}
	if _, _, err := CheckGeminiBilling(context.Background(), p); err != nil || calls != 2 {
		t.Fatalf("explicit check must refresh: calls=%d error=%v", calls, err)
	}
	credits := 180.0
	p.GeminiBilling = geminiBillingFixture()
	p.GeminiBilling.Credits = &credits
	r := geminiBillingRead(context.Background(), p, false)
	if calls != 3 || r.amount != "≈ HK$169.65" {
		t.Fatalf("top-up edit did not recalculate: %+v calls=%d", r, calls)
	}
	report := quotaReport(nil, nil, qs, time.Now())
	if report[0].BillingEstimate == nil {
		t.Fatal("CLI and remote cards lost the estimate's source timestamps")
	}
}

func TestGeminiBillingSetupAndValidation(t *testing.T) {
	isolate(t)
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	t.Setenv("CLOUDSDK_CONFIG", t.TempDir())
	p, _ := FromPreset("google")
	p.Key = "model-key"
	amount, supported, err := Balance(context.Background(), p)
	if !supported || err == nil || amount != "" {
		t.Fatalf("missing setup must not show zero: %q %v %v", amount, supported, err)
	}
	if _, err := geminiBillingToken(context.Background()); err != errGeminiBillingAuth {
		t.Fatalf("missing ADC needs a sign-in instruction: %v", err)
	}
	for _, mutate := range []func(*GeminiBilling){
		func(c *GeminiBilling) { c.Table += "`; DELETE FROM private" },
		func(c *GeminiBilling) { c.BillingAccount = "another-account" },
		func(c *GeminiBilling) { c.ServiceID = "Vertex" },
		func(c *GeminiBilling) { c.Since = "2026-02-30" },
		func(c *GeminiBilling) { c.Currency = "dollars" },
		func(c *GeminiBilling) { n := math.Inf(1); c.Credits = &n },
		func(c *GeminiBilling) { c.Credits = nil },
	} {
		c := geminiBillingFixture()
		mutate(c)
		p.GeminiBilling = c
		if err := Save(p); err == nil {
			t.Fatalf("invalid configuration saved: %+v", c)
		}
	}
	p.GeminiBilling = geminiBillingFixture()
	p.Chat = "https://relay.example/v1"
	if err := Save(p); err == nil {
		t.Fatal("a relay must not be matched to Google's billing account")
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	geminiBillingGate <- struct{}{}
	r := geminiBillingRead(ctx, Provider{Chat: "https://generativelanguage.googleapis.com/v1beta/openai", GeminiBilling: geminiBillingFixture()}, false)
	<-geminiBillingGate
	if r.err != context.Canceled {
		t.Fatalf("canceled reads should not wait on the query gate: %v", r.err)
	}
}

func TestGeminiBillingErrorsKeepScope(t *testing.T) {
	isolate(t)
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	t.Cleanup(ForgetBalances)
	oldToken, oldClient := readGeminiBillingToken, http.DefaultClient
	t.Cleanup(func() { readGeminiBillingToken, http.DefaultClient = oldToken, oldClient })
	readGeminiBillingToken = func(context.Context) (string, error) { return "billing-secret", nil }
	geminiBillingCache = map[string]geminiBillingReading{}
	status := 200
	http.DefaultClient = &http.Client{Transport: volcengineBalanceTransport(func(*http.Request) (*http.Response, error) {
		body := geminiBillingReply
		if status != 200 {
			body = `{"error":{"message":"billing-secret"}}`
		}
		return &http.Response{StatusCode: status, Header: http.Header{}, Body: io.NopCloser(strings.NewReader(body))}, nil
	})}
	p, _ := FromPreset("google")
	p.Key, p.GeminiBilling = "model-key", geminiBillingFixture()
	if err := Save(p); err != nil {
		t.Fatal(err)
	}
	if qs := KeyBalances(context.Background()); len(qs) != 1 || qs[0].Balance == "" {
		t.Fatalf("first estimate: %+v", qs)
	}
	status = 500
	ctx := context.WithValue(context.Background(), cardRefreshKey{}, cardRefresh{provider: p.ID})
	qs := KeyBalances(ctx)
	if len(qs) != 1 || qs[0].AsOf == nil || qs[0].BillingEstimate == nil || qs[0].Balance != "≈ HK$149.65" {
		t.Fatalf("transient failure must keep a dated estimate: %+v", qs)
	}
	p.GeminiBilling = geminiBillingFixture()
	p.GeminiBilling.BillingAccount = "111111-222222-333333"
	if err := Save(p); err != nil {
		t.Fatal(err)
	}
	qs = KeyBalances(ctx)
	if len(qs) != 1 || qs[0].Balance != "" || qs[0].AsOf != nil || strings.Contains(qs[0].Error, "billing-secret") {
		t.Fatalf("another account must not inherit the old estimate or leak errors: %+v", qs)
	}
	status = 403
	if _, _, err := CheckGeminiBilling(context.Background(), p); err == nil || !strings.Contains(err.Error(), "HTTP 403") || strings.Contains(err.Error(), "billing-secret") {
		t.Fatalf("permission errors must be useful and redacted: %v", err)
	}
}
