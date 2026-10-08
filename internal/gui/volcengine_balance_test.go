package gui

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/yetone/magpie/internal/provider"
)

func TestVolcengineBillingEditorCredentials(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	p, _ := provider.FromPreset("volcengine")
	p.Key, p.BalanceToken = "ark-model-key", "test-access:test-secret"
	if err := provider.Save(p); err != nil {
		t.Fatal(err)
	}
	view := providerInfo(p, nil)
	b, _ := json.Marshal(view)
	if !view.BalanceToken.Takes || !view.BalanceToken.Set || view.BalanceToken.Kind != "volcengine" || strings.Contains(string(b), "test-access") || strings.Contains(string(b), "test-secret") {
		t.Fatalf("editor must receive only masked billing metadata: %s", b)
	}
	mux := http.NewServeMux()
	providerRoutes(mux, nil)
	for _, c := range []struct{ extra, want string }{
		{``, "test-access:test-secret"},
		{`,"balanceToken":"test-access-new:test-secret-new"`, "test-access-new:test-secret-new"},
		{`,"clearBalanceToken":true`, ""},
	} {
		w := httptest.NewRecorder()
		mux.ServeHTTP(w, httptest.NewRequest("POST", "/api/provider/save", strings.NewReader(`{"id":"volcengine","from":"volcengine","name":"Volcengine Ark","preset":"volcengine","key":"","chat":"`+p.Chat+`","models":[],"headers":{}`+c.extra+`}`)))
		if w.Code != 200 || strings.Contains(w.Body.String(), "test-secret") {
			t.Fatalf("save must keep credentials out of the reply: %d %s", w.Code, w.Body)
		}
		got, err := provider.Find(p.ID)
		if err != nil || got.BalanceToken != c.want || got.Key != p.Key {
			t.Fatalf("billing credentials were not preserved/replaced/removed independently of the model key: %v", err)
		}
	}
}
