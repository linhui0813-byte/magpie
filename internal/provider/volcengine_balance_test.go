package provider

import (
	"context"
	"errors"
	"io"
	"net/http"
	"strings"
	"testing"
	"time"
)

type volcengineBalanceTransport func(*http.Request) (*http.Response, error)

func (f volcengineBalanceTransport) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

func TestVolcengineBalanceNeedsBillingCredentials(t *testing.T) {
	p, _ := FromPreset("volcengine")
	p.Key = "ark-test-model-key"
	amount, ok, err := Balance(context.Background(), p)
	if !ok || err == nil || !strings.Contains(err.Error(), "billing credentials") || amount != "" {
		t.Fatalf("Ark balance must explain the missing billing credentials: amount=%q supported=%v error=%v", amount, ok, err)
	}
	if !TakesBalanceToken(p) {
		t.Fatal("Ark's provider editor must offer account balance credentials")
	}
}

func TestVolcengineBalanceEndpoints(t *testing.T) {
	for _, field := range []string{"chat", "responses", "anthropic"} {
		for _, path := range []string{"/api/v3", "/api/coding/v3", "/api/plan/v3"} {
			p := Provider{ID: "imported-ark", Key: "ark-test"}
			base := "https://ark.cn-beijing.volces.com" + path
			switch field {
			case "chat":
				p.Chat = base
			case "responses":
				p.Responses = base
			case "anthropic":
				p.Anthropic = base
			}
			if !UsesVolcengineBilling(p) || !TakesBalanceToken(p) {
				t.Errorf("Ark billing not recognized for %s %s", field, base)
			}
		}
	}
	for _, base := range []string{"https://ark.cn-beijing.volces.com.evil.example/api/v3", "https://api.volces.com/api/v3", "https://relay.example/api/v3"} {
		if UsesVolcengineBilling(Provider{ID: "volcengine", Chat: base}) {
			t.Errorf("unrelated host recognized as Ark: %s", base)
		}
	}
	p := Provider{Chat: "https://ark.cn-beijing.volces.com/api/v3", BalanceURL: "https://relay.example/balance"}
	src, ok := balanceSourceOf(p)
	if UsesVolcengineBilling(p) || !ok || src.url != p.BalanceURL {
		t.Fatal("a custom Balance URL must override Ark billing")
	}
}

func TestReadVolcengineBalance(t *testing.T) {
	for _, c := range []struct{ body, want string }{
		{`{"Result":{"AvailableBalance":"34.95","CashBalance":"99.99","Currency":"CNY"}}`, "¥34.95"},
		{`{"Result":{"AvailableBalance":0,"Currency":"CNY"}}`, "¥0.00"},
		{`{"Result":{"AvailableBalance":"-1.25"}}`, "¥-1.25"},
		{`{"Result":{"AvailableBalance":"1.235","Currency":"USD"}}`, "$1.24"},
		{`{"ResponseMetadata":{"Error":null},"Result":{"AvailableBalance":"2","Currency":"EUR"}}`, "EUR 2.00"},
	} {
		got, err := readVolcengineBalance([]byte(c.body))
		if got != c.want || err != nil {
			t.Errorf("%s: balance=%q error=%v, want %q", c.body, got, err, c.want)
		}
	}
	for _, body := range []string{`{`, `{}`, `null`, `{"Result":{"CashBalance":"99"}}`, `{"Result":{"AvailableBalance":null}}`, `{"Result":{"AvailableBalance":""}}`, `{"Result":{"AvailableBalance":true}}`, `{"Result":{"AvailableBalance":"NaN"}}`, `{"Result":{"AvailableBalance":"Inf"}}`, `{"ResponseMetadata":{"Error":{"Code":"AccessDenied","Message":"test-secret"}},"Result":{"AvailableBalance":"99"}}`} {
		amount, err := readVolcengineBalance([]byte(body))
		if amount != "" || err == nil || strings.Contains(err.Error(), "test-secret") {
			t.Errorf("invalid/refused balance must remain unknown without echoing credentials: amount=%q error=%v", amount, err)
		}
	}
}

func TestVolcengineBalanceSignature(t *testing.T) {
	req, _ := http.NewRequest("POST", volcengineBalanceURL, strings.NewReader("{}"))
	signVolcengineBalance(req, "test-access", "test-secret", time.Date(2026, 10, 8, 9, 2, 3, 0, time.FixedZone("CST", 8*3600)))
	// Generated independently with the official Python SDK's SignerV4.sign
	// for POST {}, cn-beijing/billing, at 2026-10-08T01:02:03Z.
	const want = "HMAC-SHA256 Credential=test-access/20261008/cn-beijing/billing/request, SignedHeaders=content-type;host;x-content-sha256;x-date, Signature=f773e29db676d0d284415020605d4c541bbc3af9404308fa643edf36e44574e0"
	if req.Header.Get("Authorization") != want || req.Header.Get("X-Date") != "20261008T010203Z" {
		t.Fatalf("billing signature does not match the official SDK: %s", req.Header.Get("Authorization"))
	}
}

func TestVolcengineBalanceCredentialsStayAtBilling(t *testing.T) {
	old := http.DefaultClient.Transport
	t.Cleanup(func() { http.DefaultClient.Transport = old })
	calls := 0
	http.DefaultClient.Transport = volcengineBalanceTransport(func(r *http.Request) (*http.Response, error) {
		calls++
		if r.URL.Host != "open.volcengineapi.com" {
			t.Error("billing credentials were forwarded to another host")
		}
		return &http.Response{StatusCode: 302, Body: io.NopCloser(strings.NewReader("")), Header: http.Header{"Location": {"https://elsewhere.example/balance"}}}, nil
	})
	for _, token := range []string{"ark-model-key", ":test-secret", "test-access:", "test-access:test-secret:extra", "test-access:test-secret\nextra"} {
		amount, err := volcengineBalance(context.Background(), token)
		if amount != "" || !errors.Is(err, errVolcengineBilling) {
			t.Errorf("invalid credentials must give a setup instruction without sending a request: amount=%q error=%v", amount, err)
		}
	}
	if calls != 0 {
		t.Fatal("malformed credentials were sent to billing")
	}
	amount, err := volcengineBalance(context.Background(), "test-access:test-secret")
	if amount != "" || err == nil || !strings.Contains(err.Error(), "HTTP 302") || calls != 1 {
		t.Fatalf("a redirect must remain a failed lookup without forwarding auth: amount=%q error=%v calls=%d", amount, err, calls)
	}
}

func TestVolcengineBalanceOnUsageCard(t *testing.T) {
	isolate(t)
	ForgetBalances()
	t.Cleanup(ForgetBalances)
	old := http.DefaultClient.Transport
	t.Cleanup(func() { http.DefaultClient.Transport = old })
	calls := 0
	status, body := 200, `{"ResponseMetadata":{"Action":"QueryBalanceAcct","Version":"2022-01-01","Service":"billing"},"Result":{"AccountID":123456,"AvailableBalance":"34.95","CashBalance":"99.99","CreditLimit":"0","FreezeAmount":"65.04","ArrearsBalance":"0","Currency":"CNY"}}`
	var failure error
	http.DefaultClient.Transport = volcengineBalanceTransport(func(r *http.Request) (*http.Response, error) {
		calls++
		if r.URL.String() != "https://open.volcengineapi.com/?Action=QueryBalanceAcct&Version=2022-01-01" || r.Method != "POST" {
			t.Errorf("billing request = %s %s", r.Method, r.URL)
		}
		requestBody, _ := io.ReadAll(r.Body)
		if string(requestBody) != "{}" || !strings.HasPrefix(r.Header.Get("Authorization"), "HMAC-SHA256 Credential=test-access") || r.Header.Get("X-Date") == "" || r.Header.Get("X-Content-Sha256") == "" {
			t.Error("billing lookup must be signed with its AK/SK")
		}
		if strings.Contains(r.Header.Get("Authorization"), "ark-test-model-key") || r.Header.Get("X-Model-Header") != "" {
			t.Error("model authentication must never go to billing")
		}
		if failure != nil {
			return nil, failure
		}
		return &http.Response{StatusCode: status, Body: io.NopCloser(strings.NewReader(body)), Header: make(http.Header)}, nil
	})
	p, _ := FromPreset("volcengine")
	p.Key, p.KeyName, p.BalanceToken = "ark-test-model-key", "main", "test-access:test-secret"
	p.Headers = map[string]string{"X-Model-Header": "model-only"}
	p.Keys = []KeyAccount{{Name: "spare", Key: "ark-spare"}}
	if err := Save(p); err != nil {
		t.Fatal(err)
	}
	qs := KeyBalances(context.Background())
	if len(qs) != 1 || qs[0].Provider != p.ID || qs[0].Balance != "¥34.95" || qs[0].Error != "" || qs[0].ReadAt == nil {
		t.Fatalf("Usage must show one Ark account balance with its reading time: %+v", qs)
	}
	if calls != 1 {
		t.Fatalf("shared balance was read %d times for two model keys", calls)
	}
	// A refresh uses the existing card and a temporary failure preserves its
	// dated reading. Changing billing accounts must not inherit that balance.
	failure = errors.New("network is unreachable")
	RefreshUsage(context.Background(), p.ID, "")
	qs = KeyBalances(context.Background())
	if len(qs) != 1 || qs[0].Balance != "¥34.95" || qs[0].AsOf == nil {
		t.Fatalf("temporary failure lost the dated account balance: %+v", qs)
	}
	p.BalanceToken = "test-access-new:test-secret-new"
	if err := Save(p); err != nil {
		t.Fatal(err)
	}
	ForgetBalances() // the provider save API invalidates its balance cache
	qs = KeyBalances(context.Background())
	if len(qs) != 1 || qs[0].Balance != "" || qs[0].Error == "" {
		t.Fatalf("new billing account inherited the previous balance: %+v", qs)
	}
	failure, status, body = nil, 403, `{"ResponseMetadata":{"Error":{"Code":"AccessDenied","Message":"test-secret-new"}}}`
	ForgetBalances()
	qs = KeyBalances(context.Background())
	if len(qs) != 1 || qs[0].Balance != "" || qs[0].Error == "" || strings.Contains(qs[0].Error, "test-secret-new") {
		t.Fatalf("billing refusal must not show a balance or credentials: %+v", qs)
	}
}
