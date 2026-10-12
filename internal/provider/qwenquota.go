package provider

// Qwen Token Plan's allowance belongs to the signed-in Bailian console,
// not to its model API key. Reuse the official CLI's console sign-in without
// copying it; the CLI owns renewal too. Its gateway serves the monthly plan.

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/yetone/magpie/internal/appdir"
)

const qwenUsageAPI = "zeldaHttp.apikeyMgr./tokenplan/personal/api/v2/usage"

var errQwenConsole = errors.New("Qwen Token Plan needs a Bailian console sign-in; run bl auth login --console")

type qwenConsole struct {
	Token       string `json:"access_token"`
	Region      string `json:"console_region"`
	Site        string `json:"console_site"`
	SwitchAgent int64  `json:"console_switch_agent"`
	Profile     string `json:"-"`
	Dir         string `json:"-"`
	CanRenew    bool   `json:"-"`
}

// readQwenConsole follows the CLI's active_config, including named profiles.
// A corrupt or missing active profile is an error, never another login.
func readQwenConsole() (qwenConsole, error) {
	dir := appdir.Getenv("BAILIAN_CONFIG_DIR")
	if dir == "" {
		home, err := appdir.Home()
		if err != nil {
			return qwenConsole{}, err
		}
		dir = filepath.Join(home, ".bailian")
	}
	b, err := os.ReadFile(filepath.Join(dir, "config.json"))
	if errors.Is(err, os.ErrNotExist) {
		return qwenConsole{}, errQwenConsole
	}
	if err != nil {
		return qwenConsole{}, errors.New("could not read the Bailian CLI configuration")
	}
	var root map[string]json.RawMessage
	if json.Unmarshal(b, &root) != nil || root == nil {
		return qwenConsole{}, errors.New("could not decode the Bailian CLI configuration")
	}
	profile := "default"
	if raw, ok := root["active_config"]; ok {
		if json.Unmarshal(raw, &profile) != nil || profile == "" {
			return qwenConsole{}, errors.New("invalid Bailian CLI active profile")
		}
	}
	if profile != "default" {
		var ok bool
		if b, ok = root[profile]; !ok {
			return qwenConsole{}, errors.New("the Bailian CLI active profile is missing")
		}
	}
	var c qwenConsole
	if json.Unmarshal(b, &c) != nil {
		return c, errors.New("could not decode the Bailian CLI console sign-in")
	}
	c.Token = strings.TrimSpace(c.Token)
	c.Profile = profile
	c.Dir = dir
	if c.Token == "" {
		return c, errQwenConsole
	}
	if c.Region != "" && c.Region != "cn-beijing" || c.Site != "" && c.Site != "domestic" {
		return c, errors.New("Qwen Token Plan needs a domestic Beijing Bailian console profile")
	}
	// The CLI renews from a stored static AccessKey pair, not from a
	// browser refresh token. Keep only whether that pair is configured.
	var auth struct {
		ID     string `json:"access_key_id"`
		Secret string `json:"access_key_secret"`
		STS    string `json:"security_token"`
	}
	if json.Unmarshal(b, &auth) == nil {
		c.CanRenew = strings.TrimSpace(auth.ID) != "" && strings.TrimSpace(auth.Secret) != "" && strings.TrimSpace(auth.STS) == ""
	}
	return c, nil
}

func qwenTokenPlan(p Provider) bool {
	for _, base := range []string{p.Chat, p.Anthropic, p.Responses} {
		if hostOf(base) == "token-plan.maas.qianwenaiapi.com" {
			return true
		}
	}
	return false
}

// qwenPlanQuotas shows the console's shared personal allowance once, on
// the first enabled Token Plan provider. It does not assign that account's
// windows to individual API keys or use them to stop gateway routing.
func qwenPlanQuotas(ctx context.Context) []SubscriptionQuota {
	for _, p := range All() {
		if p.Hidden || p.Off || p.Account != nil || p.Key == "" || !qwenTokenPlan(p) {
			continue
		}
		const user = "Bailian console"
		if !wantsCard(ctx, p.ID, user) {
			return nil
		}
		q := SubscriptionQuota{Provider: p.ID, Name: p.Name, Icon: p.Icon, Plan: "Token Plan", User: user, Windows: []QuotaWindow{}}
		c, err := readQwenConsole()
		if err == nil {
			c, q.Windows, err = qwenConsoleAllowance(p.Via(ctx), c)
		}
		if err != nil {
			q.Error = err.Error()
		}
		// A profile or session changed must never inherit another account's
		// last reading. Neither the token nor its payload goes on the card.
		if c.Token != "" {
			q = keepReading(ctx, q, keyTag("qwen-console", c.Token+"/"+c.Profile+fmt.Sprint(c.SwitchAgent)))
		}
		return []SubscriptionQuota{q}
	}
	return nil
}

// qwenConsoleWindows sends the same gateway request as modelstudioai/cli's
// usage/token-plan.ts and console/gateway.ts. The API key is never sent here.
func qwenConsoleWindows(ctx context.Context, c qwenConsole) ([]QuotaWindow, error) {
	corner := map[string]any{"protocol": "V2", "console": "ONE_CONSOLE", "productCode": "p_efm", "switchUserType": 3, "consoleSite": "BAILIAN_ALIYUN"}
	if c.SwitchAgent != 0 {
		corner["switchAgent"] = c.SwitchAgent
	}
	params, err := json.Marshal(map[string]any{"Api": qwenUsageAPI, "V": "1.0", "Data": map[string]any{"cornerstoneParam": corner}})
	if err != nil {
		return nil, err
	}
	query := url.Values{"action": {"BroadScopeAspnGateway"}, "product": {"sfm_bailian"}, "api": {qwenUsageAPI}}
	body := url.Values{"params": {string(params)}, "region": {"cn-beijing"}}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, "https://bailian-cs.console.aliyun.com/cli/api.json?"+query.Encode(), strings.NewReader(body.Encode()))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Authorization", "Bearer "+c.Token)
	req.Header.Set("Accept", "*/*")
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	res, err := http.DefaultClient.Do(req)
	if err != nil {
		return nil, err
	}
	defer res.Body.Close()
	if res.StatusCode == http.StatusUnauthorized {
		return nil, errQwenConsole
	}
	if res.StatusCode >= 300 {
		return nil, fmt.Errorf("Qwen console: HTTP %d", res.StatusCode)
	}
	b, err := io.ReadAll(io.LimitReader(res.Body, (1<<20)+1))
	if err != nil {
		return nil, err
	}
	if len(b) > 1<<20 {
		return nil, errors.New("Qwen console reply is too large")
	}
	return readQwenPlan(b)
}

// readQwenPlan unwraps the console's DataV2/data envelope. The vendor's
// Percentage fields are fractions (0.25 means 25% used), and ResetTime
// is milliseconds. Missing windows are unknown, not empty or unlimited.
func readQwenPlan(b []byte) ([]QuotaWindow, error) {
	for depth := 0; depth < 8; depth++ {
		var r map[string]json.RawMessage
		if json.Unmarshal(b, &r) != nil || r == nil {
			return nil, errors.New("could not decode Qwen Token Plan allowance")
		}
		var success *bool
		if raw, ok := r["success"]; ok && (json.Unmarshal(raw, &success) != nil || success != nil && !*success) {
			var code string
			_ = json.Unmarshal(r["errorCode"], &code)
			if strings.Contains(code, "NotLogined") {
				return nil, errQwenConsole
			}
			if code == "BailianGateway.AuthorityPolicies.NoPermission" {
				return nil, errors.New("Qwen Token Plan console permission denied; check this RAM user's permissions in Bailian account management")
			}
			return nil, errors.New("Qwen Token Plan console lookup was refused")
		}
		var ws []QuotaWindow
		for _, x := range []struct {
			field, name string
			span        time.Duration
		}{{"per5Hour", "5 hours", 5 * time.Hour}, {"per1Week", "7 days", 7 * 24 * time.Hour}, {"per1Month", "Month", 0}} {
			raw, ok := r[x.field+"Percentage"]
			if !ok || string(raw) == "null" {
				continue
			}
			var share float64
			if json.Unmarshal(raw, &share) != nil || math.IsNaN(share) || math.IsInf(share, 0) || share < 0 {
				return nil, errors.New("invalid Qwen Token Plan allowance share")
			}
			w := QuotaWindow{Name: x.name, Used: min(share*100, 100), Span: x.span}
			if raw, ok := r[x.field+"ResetTime"]; ok && string(raw) != "null" {
				var ms int64
				if json.Unmarshal(raw, &ms) != nil || ms < 0 || ms > 253402300799999 { // last RFC3339 millisecond
					return nil, errors.New("invalid Qwen Token Plan reset time")
				}
				if ms > 0 {
					at := time.UnixMilli(ms)
					w.ResetsAt = &at
				}
			}
			ws = append(ws, w)
		}
		if len(ws) > 0 {
			return ws, nil
		}
		if next, ok := r["DataV2"]; ok {
			b = next
		} else if next, ok := r["data"]; ok {
			b = next
		} else {
			break
		}
	}
	return nil, errors.New("Qwen Token Plan reported no allowance; check the Bailian console")
}
