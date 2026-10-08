package testenv

import (
	"context"
	"errors"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
)

func TestLocal(t *testing.T) {
	for addr, want := range map[string]bool{
		"127.0.0.1:80": true, "[::1]:443": true, "localhost:3425": true, "LOCALHOST.:1": true,
		"magpie.localhost:80": true, "0.0.0.0:80": true, "[::]:80": true, "127.8.0.1:9": true,
		"models.dev:443": false, "api.kimi.com:443": false, "10.0.0.132:22": false,
		"192.168.1.2:80": false, "localhost.example.com:80": false, "decide.test:443": false,
	} {
		if got := local(addr); got != want {
			t.Errorf("local(%q) = %v, want %v", addr, got, want)
		}
	}
}

// Offline lets a loopback server answer through http.DefaultClient and
// refuses a vendor's host without dialing it.
func TestOffline(t *testing.T) {
	tr := http.DefaultTransport.(*http.Transport)
	was := tr.DialContext
	proxy := tr.Proxy
	t.Cleanup(func() { tr.DialContext, tr.Proxy = was, proxy })
	Offline()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.Write([]byte("here")) }))
	defer srv.Close()
	resp, err := http.Get(srv.URL)
	if err != nil {
		t.Fatalf("loopback refused: %v", err)
	}
	resp.Body.Close()
	clone := tr.Clone() // a transport made from it after the call
	t.Cleanup(clone.CloseIdleConnections)
	for _, c := range []*http.Client{http.DefaultClient, {Transport: clone}} {
		_, err := c.Get("https://models.dev/api.json")
		if err == nil || !strings.Contains(err.Error(), "tests stay off the network") {
			t.Errorf("models.dev: %v", err)
		}
	}
}

// A loopback proxy can forward to a vendor without ever dialing its host
// from this process. Refuse the request before selecting or dialing it.
func TestOfflineProxy(t *testing.T) {
	for _, proxied := range []bool{false, true} {
		t.Run(map[bool]string{false: "direct", true: "loopback proxy"}[proxied], func(t *testing.T) {
			was := http.DefaultTransport
			tr := &http.Transport{}
			http.DefaultTransport = tr
			t.Cleanup(func() { http.DefaultTransport = was })
			t.Cleanup(tr.CloseIdleConnections)
			proxyCalls, dials := 0, 0
			if proxied {
				tr.Proxy = func(*http.Request) (*url.URL, error) {
					proxyCalls++
					return url.Parse("http://127.0.0.1:7890")
				}
			}
			stopped := errors.New("fixture dial stopped")
			tr.DialContext = func(context.Context, string, string) (net.Conn, error) {
				dials++
				return nil, stopped
			}
			Offline()
			clone := tr.Clone()
			t.Cleanup(clone.CloseIdleConnections)
			for _, transport := range []*http.Transport{tr, clone} {
				for _, scheme := range []string{"http", "https"} {
					req, _ := http.NewRequest(http.MethodGet, scheme+"://models.dev/api.json", nil)
					if _, err := transport.RoundTrip(req); err == nil || !strings.Contains(err.Error(), "tests stay off the network") {
						t.Errorf("%s vendor request escaped the offline guard: %v", scheme, err)
					}
				}
			}
			if proxyCalls != 0 || dials != 0 {
				t.Errorf("vendor request reached the proxy or dialer: proxy=%d, dial=%d", proxyCalls, dials)
			}
			// Local fixtures still use the original proxy and dialer.
			req, _ := http.NewRequest(http.MethodGet, "http://127.0.0.1:3425", nil)
			if _, err := tr.RoundTrip(req); !errors.Is(err, stopped) {
				t.Errorf("local fixture did not reach the original dialer: %v", err)
			}
			wantProxy := 0
			if proxied {
				wantProxy = 1
			}
			if proxyCalls != wantProxy || dials != 1 {
				t.Errorf("local fixture: proxy=%d, dial=%d", proxyCalls, dials)
			}
		})
	}
}
