package gui

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/yetone/magpie/internal/provider"
)

const geminiBillingFixture = `{"table":"billing-project.billing.gcp_billing_export_v1_AAAAAA_BBBBBB_CCCCCC","location":"US","billingAccount":"AAAAAA-BBBBBB-CCCCCC","serviceID":"ABCD-1234-5678","currency":"HKD","since":"2026-10-12","credits":160}`

func TestGeminiBillingEditorKeepsEstimateSettings(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	p, _ := provider.FromPreset("google")
	p.Key = "gemini-test-model-key"
	if err := provider.Save(p); err != nil {
		t.Fatal(err)
	}
	mux := http.NewServeMux()
	providerRoutes(mux, nil)
	for _, extra := range []string{`,"geminiBilling":` + geminiBillingFixture, ""} {
		w := httptest.NewRecorder()
		mux.ServeHTTP(w, httptest.NewRequest("POST", "/api/provider/save", strings.NewReader(`{"id":"google","preset":"google","key":""`+extra+`}`)))
		if w.Code != 200 {
			t.Fatalf("save: %d %s", w.Code, w.Body)
		}
		got, err := provider.Find("google")
		if err != nil || got.Key != p.Key {
			t.Fatalf("estimate setup changed the model key: %v", err)
		}
		b, _ := json.Marshal(providerInfo(*got, nil))
		var view map[string]json.RawMessage
		_ = json.Unmarshal(b, &view)
		if !strings.Contains(string(view["geminiBilling"]), `"credits":160`) || string(view["geminiBillingSupported"]) != "true" {
			t.Fatalf("Gemini editor must preserve and expose its billing estimate settings: %s", b)
		}
	}
	w := httptest.NewRecorder()
	mux.ServeHTTP(w, httptest.NewRequest("POST", "/api/provider/save", strings.NewReader(`{"id":"google","preset":"google","geminiBilling":{}}`)))
	got, err := provider.Find("google")
	if w.Code != 200 || err != nil || got.GeminiBilling != nil || got.Key != p.Key {
		t.Fatalf("turning off the estimate must clear only its settings: status=%d error=%v", w.Code, err)
	}
	w = httptest.NewRecorder()
	bad := strings.Replace(geminiBillingFixture, `"credits":160`, `"credits":-1`, 1)
	mux.ServeHTTP(w, httptest.NewRequest("POST", "/api/provider/save", strings.NewReader(`{"id":"google","preset":"google","geminiBilling":`+bad+`}`)))
	got, err = provider.Find("google")
	if w.Code == 200 || err != nil || got.GeminiBilling != nil || got.Key != p.Key {
		t.Fatalf("invalid estimate settings must not alter the saved provider: status=%d error=%v", w.Code, err)
	}
}
