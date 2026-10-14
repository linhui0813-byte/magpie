package provider

// Gemini's prepaid wallet has no documented balance API. This estimates
// recorded funds less net charges in an account's Cloud Billing export.

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"runtime"
	"strconv"
	"strings"
	"time"

	"github.com/yetone/magpie/internal/appdir"
	"golang.org/x/oauth2/google"
)

// GeminiBilling describes a standard billing export, not model credentials.
// Credits includes the balance at Since (00:00 UTC) and all later top-ups.
type GeminiBilling struct {
	Table          string   `json:"table"`
	Location       string   `json:"location"`
	BillingAccount string   `json:"billingAccount"`
	ServiceID      string   `json:"serviceID"`
	Currency       string   `json:"currency"`
	Since          string   `json:"since"`
	Credits        *float64 `json:"credits"`
}

// BillingEstimate dates the exported rows used; LatestUsage is the newest
// usage row, not a guarantee that all charges through that time are present.
type BillingEstimate struct {
	ExportedAt  time.Time `json:"exportedAt"`
	LatestUsage time.Time `json:"latestUsage"`
}

var geminiTableID = regexp.MustCompile(`^[a-z][a-z0-9-]{4,61}[a-z0-9]\.[A-Za-z0-9_]+\.[A-Za-z0-9_]+$`)
var geminiAccountID = regexp.MustCompile(`^[A-F0-9]{6}-[A-F0-9]{6}-[A-F0-9]{6}$`)
var geminiServiceID = regexp.MustCompile(`^[A-F0-9]{4}-[A-F0-9]{4}-[A-F0-9]{4}$`)
var geminiLocation = regexp.MustCompile(`^[A-Za-z][A-Za-z0-9-]{0,63}$`)
var geminiCurrency = regexp.MustCompile(`^[A-Z]{3}$`)

var errGeminiBillingSetup = errors.New("Gemini billing estimate needs setup; configure the BigQuery billing export in this provider's Credit estimate field")
var errGeminiBillingAuth = errors.New("Gemini billing estimate needs Google Cloud credentials; run gcloud auth application-default login, or configure GOOGLE_APPLICATION_CREDENTIALS")

// SupportsGeminiBilling includes an imported provider using Google's own
// endpoint, but never a relay or the separate Vertex AI service.
func SupportsGeminiBilling(p Provider) bool {
	for _, base := range []string{p.Chat, p.Responses, p.Anthropic} {
		if hostOf(base) == "generativelanguage.googleapis.com" {
			return p.Account == nil
		}
	}
	return false
}

func (c *GeminiBilling) normal() *GeminiBilling {
	if c == nil || *c == (GeminiBilling{}) {
		return nil
	}
	out := *c
	out.Table, out.Location, out.Since = strings.TrimSpace(c.Table), strings.TrimSpace(c.Location), strings.TrimSpace(c.Since)
	out.BillingAccount, out.ServiceID, out.Currency = strings.ToUpper(strings.TrimSpace(c.BillingAccount)), strings.ToUpper(strings.TrimSpace(c.ServiceID)), strings.ToUpper(strings.TrimSpace(c.Currency))
	return &out
}

func (c *GeminiBilling) check() error {
	if c == nil {
		return errGeminiBillingSetup
	}
	if !geminiTableID.MatchString(c.Table) || !geminiLocation.MatchString(c.Location) || !geminiAccountID.MatchString(c.BillingAccount) || !geminiServiceID.MatchString(c.ServiceID) || !geminiCurrency.MatchString(c.Currency) {
		return errors.New("Gemini billing estimate needs a valid project.dataset.table, location, billing account ID, Gemini service ID and currency")
	}
	if _, err := time.Parse("2006-01-02", c.Since); err != nil {
		return errors.New("Gemini billing estimate start date must be YYYY-MM-DD (00:00 UTC)")
	}
	if c.Credits == nil || math.IsNaN(*c.Credits) || math.IsInf(*c.Credits, 0) || *c.Credits < 0 || *c.Credits > 1e12 {
		return errors.New("Gemini billing estimate needs recorded funds between 0 and 1000000000000, in the export's currency")
	}
	return nil
}

type geminiBillingReading struct {
	amount   string
	estimate *BillingEstimate
	at       time.Time
	err      error
}

// Serializing reads coalesces the page, tray and CLI's queries. No lock is
// held while Google answers. Changes to any setting get a different entry.
var geminiBillingGate = make(chan struct{}, 1)
var geminiBillingCache = map[string]geminiBillingReading{}
var readGeminiBillingToken = geminiBillingToken

func geminiBillingRead(ctx context.Context, p Provider, force bool) geminiBillingReading {
	c := p.GeminiBilling.normal()
	if !SupportsGeminiBilling(p) || p.BalanceURL != "" {
		return geminiBillingReading{err: errGeminiBillingSetup}
	}
	if err := c.check(); err != nil {
		return geminiBillingReading{err: err}
	}
	select {
	case geminiBillingGate <- struct{}{}:
		defer func() { <-geminiBillingGate }()
	case <-ctx.Done():
		return geminiBillingReading{err: ctx.Err()}
	}
	b, _ := json.Marshal(c)
	key := string(b)
	if old, ok := geminiBillingCache[key]; ok && !force && time.Since(old.at) < 15*time.Minute {
		return old
	}
	r := queryGeminiBilling(ctx, c)
	r.at = time.Now()
	geminiBillingCache[key] = r
	return r
}

// CheckGeminiBilling checks the form without saving it. An explicit check
// or Usage-card refresh bypasses the fifteen-minute query cache.
func CheckGeminiBilling(ctx context.Context, p Provider) (string, *BillingEstimate, error) {
	r := geminiBillingRead(ctx, p, true)
	return r.amount, r.estimate, r.err
}

func geminiBillingToken(ctx context.Context) (string, error) {
	path := appdir.Getenv("GOOGLE_APPLICATION_CREDENTIALS")
	if path == "" {
		dir := appdir.Getenv("CLOUDSDK_CONFIG")
		if dir == "" {
			home, err := os.UserHomeDir()
			if err != nil {
				return "", errGeminiBillingAuth
			}
			dir = filepath.Join(home, ".config", "gcloud")
			if runtime.GOOS == "windows" {
				root := appdir.Getenv("APPDATA")
				if root == "" {
					return "", errGeminiBillingAuth
				}
				dir = filepath.Join(root, "gcloud")
			}
		}
		path = filepath.Join(dir, "application_default_credentials.json")
	}
	f, err := os.Open(path)
	if err != nil {
		return "", errGeminiBillingAuth
	}
	defer f.Close()
	b, err := io.ReadAll(io.LimitReader(f, (1<<20)+1))
	if err != nil || len(b) > 1<<20 {
		return "", errGeminiBillingAuth
	}
	// The official library refreshes access tokens from the existing ADC or
	// service-account credentials. Neither JSON nor token errors escape.
	c, err := google.CredentialsFromJSON(ctx, b, "https://www.googleapis.com/auth/bigquery")
	if err != nil {
		return "", errGeminiBillingAuth
	}
	tok, err := c.TokenSource.Token()
	if err != nil || tok == nil || tok.AccessToken == "" {
		return "", errGeminiBillingAuth
	}
	return tok.AccessToken, nil
}

func queryGeminiBilling(ctx context.Context, c *GeminiBilling) geminiBillingReading {
	ctx, cancel := context.WithTimeout(ctx, 15*time.Second)
	defer cancel()
	token, err := readGeminiBillingToken(ctx)
	if err != nil {
		return geminiBillingReading{err: err}
	}
	// Account and service scope cover all projects sharing this wallet.
	// A correlated credit sum avoids multiplying cost by the number of credits.
	query := `SELECT currency, CAST(SUM(CAST(cost AS NUMERIC) + IFNULL((SELECT SUM(CAST(c.amount AS NUMERIC)) FROM UNNEST(credits) c), 0)) AS STRING) AS net_cost,
 FORMAT_TIMESTAMP('%Y-%m-%dT%H:%M:%SZ', MAX(export_time), 'UTC') AS exported_at,
 FORMAT_TIMESTAMP('%Y-%m-%dT%H:%M:%SZ', MAX(usage_end_time), 'UTC') AS latest_usage
 FROM ` + "`" + c.Table + "`" + `
 WHERE billing_account_id = @account AND service.id = @service
 AND usage_start_time >= TIMESTAMP(@since)
 GROUP BY currency`
	param := func(name, value string) any {
		return map[string]any{"name": name, "parameterType": map[string]string{"type": "STRING"}, "parameterValue": map[string]string{"value": value}}
	}
	body, _ := json.Marshal(map[string]any{
		"query": query, "useLegacySql": false, "useQueryCache": true, "parameterMode": "NAMED",
		"queryParameters": []any{param("account", c.BillingAccount), param("service", c.ServiceID), param("since", c.Since)},
		"location":        c.Location, "maxResults": 2, "timeoutMs": 8000, "jobTimeoutMs": "10000", "maximumBytesBilled": "1073741824",
	})
	project := strings.Split(c.Table, ".")[0]
	req, _ := http.NewRequestWithContext(ctx, http.MethodPost, "https://bigquery.googleapis.com/bigquery/v2/projects/"+project+"/queries", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("X-Goog-User-Project", project)
	client := *http.DefaultClient
	client.CheckRedirect = func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }
	res, err := client.Do(req)
	if err != nil {
		return geminiBillingReading{err: errors.New("Gemini billing query fetch failed; refresh later")}
	}
	defer res.Body.Close()
	if res.StatusCode >= 300 {
		return geminiBillingReading{err: fmt.Errorf("Gemini billing query: HTTP %d; check Google credentials, table permissions and the 1 GiB query limit", res.StatusCode)}
	}
	b, err := io.ReadAll(io.LimitReader(res.Body, (1<<20)+1))
	if err != nil || len(b) > 1<<20 {
		return geminiBillingReading{err: errors.New("Gemini billing query returned an unreadable response")}
	}
	return readGeminiBilling(b, c)
}

func readGeminiBilling(b []byte, c *GeminiBilling) geminiBillingReading {
	var res struct {
		Complete bool   `json:"jobComplete"`
		Total    string `json:"totalRows"`
		Page     string `json:"pageToken"`
		Rows     []struct {
			Fields []struct {
				Value *string `json:"v"`
			} `json:"f"`
		} `json:"rows"`
	}
	bad := func(s string) geminiBillingReading { return geminiBillingReading{err: errors.New(s)} }
	if json.Unmarshal(b, &res) != nil {
		return bad("Gemini billing query returned invalid JSON")
	}
	if !res.Complete {
		return bad("Gemini billing query has not completed; refresh later")
	}
	if len(res.Rows) == 0 {
		return bad("No matching Gemini billing rows yet; check the account, service ID and export start date")
	}
	if len(res.Rows) != 1 || res.Total != "1" || res.Page != "" {
		return bad("Gemini billing export contains mixed currencies; no balance estimate was calculated")
	}
	f := res.Rows[0].Fields
	if len(f) != 4 || f[0].Value == nil || f[1].Value == nil || f[2].Value == nil || f[3].Value == nil || *f[0].Value != c.Currency {
		return bad("Gemini billing export currency or result fields do not match the estimate setup")
	}
	spent, err := strconv.ParseFloat(*f[1].Value, 64)
	if err != nil || math.IsNaN(spent) || math.IsInf(spent, 0) {
		return bad("Gemini billing export did not return a valid net charge")
	}
	exported, err1 := time.Parse(time.RFC3339, *f[2].Value)
	latest, err2 := time.Parse(time.RFC3339, *f[3].Value)
	if err1 != nil || err2 != nil {
		return bad("Gemini billing export did not return valid data timestamps")
	}
	sign := currencySign(c.Currency)
	if c.Currency == "HKD" {
		sign = "HK$"
	}
	return geminiBillingReading{amount: "≈ " + money(sign, *c.Credits-spent), estimate: &BillingEstimate{ExportedAt: exported, LatestUsage: latest}}
}
