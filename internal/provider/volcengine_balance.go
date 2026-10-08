package provider

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math"
	"net/http"
	"strings"
	"time"
)

const volcengineBalanceURL = "https://open.volcengineapi.com/?Action=QueryBalanceAcct&Version=2022-01-01"

var errVolcengineBilling = errors.New("Volcengine account balance needs billing credentials; open this provider's Account balance field and enter Access Key ID:Secret Access Key with permission to call QueryBalanceAcct. The Ark model API key cannot read billing")

// UsesVolcengineBilling recognizes Ark's own endpoints, including providers
// imported under another id. A Balance URL the user wrote takes precedence.
func UsesVolcengineBilling(p Provider) bool {
	if p.BalanceURL != "" {
		return false
	}
	for _, base := range []string{p.Chat, p.Responses, p.Anthropic} {
		if hostOf(base) == "ark.cn-beijing.volces.com" {
			return true
		}
	}
	return false
}

// volcengineBalance asks the billing account's AvailableBalance, not a
// model key's spend or a Coding/Agent Plan allowance. Billing needs its own
// AK/SK, stored as AccessKeyID:SecretAccessKey in BalanceToken. Neither the
// model key nor its headers are sent to this endpoint.
func volcengineBalance(ctx context.Context, token string) (string, error) {
	ak, sk, ok := strings.Cut(strings.TrimSpace(token), ":")
	ak, sk = strings.TrimSpace(ak), strings.TrimSpace(sk)
	if !ok || ak == "" || sk == "" || strings.ContainsAny(ak+sk, " \t\r\n:") {
		return "", errVolcengineBilling
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, volcengineBalanceURL, strings.NewReader("{}"))
	if err != nil {
		return "", err
	}
	signVolcengineBalance(req, ak, sk, time.Now())
	// A redirect must not forward the signed billing credentials elsewhere.
	client := *http.DefaultClient
	client.CheckRedirect = func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }
	res, err := client.Do(req)
	if err != nil {
		return "", fmt.Errorf("Volcengine billing: %w", err)
	}
	defer res.Body.Close()
	if res.StatusCode >= 300 {
		return "", fmt.Errorf("Volcengine billing: HTTP %d; check the billing credentials and access to QueryBalanceAcct", res.StatusCode)
	}
	b, err := io.ReadAll(io.LimitReader(res.Body, (1<<20)+1))
	if err != nil {
		return "", err
	}
	if len(b) > 1<<20 {
		return "", errors.New("Volcengine billing reply is too large")
	}
	return readVolcengineBalance(b)
}

// signVolcengineBalance follows volc-sdk-golang/base/sign.go for this one
// POST {} to the billing service in cn-beijing. The request carries only
// these signed headers; no provider header can replace the billing auth.
func signVolcengineBalance(req *http.Request, ak, sk string, now time.Time) {
	date := now.UTC().Format("20060102T150405Z")
	const contentType = "application/json; charset=utf-8"
	const signedHeaders = "content-type;host;x-content-sha256;x-date"
	hash := fmt.Sprintf("%x", sha256.Sum256([]byte("{}")))
	headers := "content-type:" + contentType + "\nhost:" + req.URL.Host + "\nx-content-sha256:" + hash + "\nx-date:" + date + "\n"
	canonical := strings.Join([]string{req.Method, "/", req.URL.RawQuery, headers, signedHeaders, hash}, "\n")
	scope := date[:8] + "/cn-beijing/billing/request"
	toSign := "HMAC-SHA256\n" + date + "\n" + scope + "\n" + fmt.Sprintf("%x", sha256.Sum256([]byte(canonical)))
	key := []byte(sk)
	for _, part := range []string{date[:8], "cn-beijing", "billing", "request", toSign} {
		mac := hmac.New(sha256.New, key)
		mac.Write([]byte(part))
		key = mac.Sum(nil)
	}
	req.Header.Set("Content-Type", contentType)
	req.Header.Set("X-Date", date)
	req.Header.Set("X-Content-Sha256", hash)
	req.Header.Set("Authorization", "HMAC-SHA256 Credential="+ak+"/"+scope+", SignedHeaders="+signedHeaders+", Signature="+fmt.Sprintf("%x", key))
}

func readVolcengineBalance(b []byte) (string, error) {
	var r struct {
		Metadata struct {
			Error json.RawMessage `json:"Error"`
		} `json:"ResponseMetadata"`
		Result struct {
			Available any    `json:"AvailableBalance"`
			Currency  string `json:"Currency"`
		} `json:"Result"`
	}
	if json.Unmarshal(b, &r) != nil {
		return "", errors.New("could not decode Volcengine account balance")
	}
	if len(r.Metadata.Error) > 0 && string(r.Metadata.Error) != "null" {
		// The vendor's message can include credentials; never echo it.
		return "", errors.New("Volcengine billing lookup was refused; check the billing credentials and access to QueryBalanceAcct")
	}
	v, ok := number(r.Result.Available)
	if !ok || math.IsNaN(v) || math.IsInf(v, 0) {
		return "", errors.New("Volcengine billing did not return a valid AvailableBalance")
	}
	sign := currencySign(r.Result.Currency)
	if sign == "" {
		// QueryBalanceAcct's domestic account balance is denominated in CNY.
		sign = "¥"
	}
	return money(sign, v), nil
}
