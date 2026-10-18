package main

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"strings"
	"testing"
	"time"
)

const relayFixtureURL = "https://relay.tribukvy.ltd/activate/SYNTHETIC_TEST_TOKEN"

type relayRoundTripper func(*http.Request) (*http.Response, error)

func (f relayRoundTripper) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

func TestRelayActivationNeverConfirmsAmbiguousSuccess(t *testing.T) {
	c := &RelayClient{transportFor: func(RelayAccess) (http.RoundTripper, error) {
		return relayRoundTripper(func(r *http.Request) (*http.Response, error) {
			if r.URL.String() != relayFixtureURL || r.Method != "GET" {
				t.Fatal("wrong activation request")
			}
			return &http.Response{StatusCode: 200, Body: io.NopCloser(strings.NewReader(`{"success":true,"ip":"198.51.100.1"}`)), Header: make(http.Header)}, nil
		}), nil
	}}
	got, err := c.Activate(context.Background(), RelayAccess{URL: relayFixtureURL, SourceIP: "192.0.2.2", PathConfirmed: true})
	if err != nil {
		t.Fatal(err)
	}
	if got.Outcome != "unverified" || got.BindingVerified || got.TunnelVerified {
		t.Fatalf("ambiguous success was certified: %+v", got)
	}
}

func TestRelayCredentialsCannotBeSerializedOrFormatted(t *testing.T) {
	a := RelayAccess{URL: relayFixtureURL, SourceIP: "192.0.2.2", PathConfirmed: true}
	b, err := json.Marshal(a)
	if err != nil {
		t.Fatal(err)
	}
	for _, s := range []string{string(b), fmt.Sprint(a), fmt.Sprintf("%+v", a), fmt.Sprintf("%#v", a)} {
		if strings.Contains(s, "SYNTHETIC_TEST_TOKEN") {
			t.Fatal("secret exposed")
		}
	}
}
func TestRelayRejectsUnsafeTargetsAndUnconfirmedSourceBeforeTransport(t *testing.T) {
	cases := []RelayAccess{
		{URL: "http://relay.tribukvy.ltd/activate/test", SourceIP: "192.0.2.2", PathConfirmed: true},
		{URL: "https://evil.example/activate/test", SourceIP: "192.0.2.2", PathConfirmed: true},
		{URL: "https://relay.tribukvy.ltd.evil.example/activate/test", SourceIP: "192.0.2.2", PathConfirmed: true},
		{URL: "https://user:secret@relay.tribukvy.ltd/activate/test", SourceIP: "192.0.2.2", PathConfirmed: true},
		{URL: "https://relay.tribukvy.ltd:443/activate/test", SourceIP: "192.0.2.2", PathConfirmed: true},
		{URL: relayFixtureURL + "?secret=1", SourceIP: "192.0.2.2", PathConfirmed: true},
		{URL: relayFixtureURL + "#fragment", SourceIP: "192.0.2.2", PathConfirmed: true},
		{URL: "https://relay.tribukvy.ltd/activate/test/extra", SourceIP: "192.0.2.2", PathConfirmed: true},
		{URL: "https://relay.tribukvy.ltd/activate/%74est", SourceIP: "192.0.2.2", PathConfirmed: true},
		{URL: "https://relay.tribukvy.ltd/activate/../test", SourceIP: "192.0.2.2", PathConfirmed: true},
		{URL: relayFixtureURL, SourceIP: "192.0.2.2"},
		{URL: relayFixtureURL, PathConfirmed: true},
		{URL: relayFixtureURL, SourceIP: "not-an-ip", PathConfirmed: true},
		{URL: relayFixtureURL, SourceIP: "0.0.0.0", PathConfirmed: true},
		{URL: relayFixtureURL, Interface: "bad/interface", PathConfirmed: true},
	}
	for i, a := range cases {
		t.Run(fmt.Sprint(i), func(t *testing.T) {
			calls := 0
			c := &RelayClient{transportFor: func(RelayAccess) (http.RoundTripper, error) {
				calls++
				return relayRoundTripper(func(*http.Request) (*http.Response, error) {
					return &http.Response{StatusCode: 200, Body: io.NopCloser(strings.NewReader("ok")), Header: make(http.Header)}, nil
				}), nil
			}}
			if _, err := c.Activate(context.Background(), a); err == nil {
				t.Error("unsafe request accepted")
			}
			if calls != 0 {
				t.Error("unsafe request reached transport")
			}
		})
	}
}

func TestRelaySanitizesTransportErrorsAndRejectsNon200OrLargeBodies(t *testing.T) {
	for _, tc := range []struct {
		name   string
		status int
		body   string
		err    error
	}{
		{name: "transport", err: fmt.Errorf("secret response %s", relayFixtureURL)},
		{name: "provider", status: 403, body: relayFixtureURL},
		{name: "oversize", status: 200, body: strings.Repeat("x", 65537)},
	} {
		t.Run(tc.name, func(t *testing.T) {
			c := &RelayClient{transportFor: func(RelayAccess) (http.RoundTripper, error) {
				return relayRoundTripper(func(*http.Request) (*http.Response, error) {
					if tc.err != nil {
						return nil, tc.err
					}
					return &http.Response{StatusCode: tc.status, Body: io.NopCloser(strings.NewReader(tc.body)), Header: make(http.Header)}, nil
				}), nil
			}}
			_, err := c.Activate(context.Background(), RelayAccess{URL: relayFixtureURL, SourceIP: "192.0.2.2", PathConfirmed: true})
			if err == nil {
				t.Fatal("failure accepted")
			}
			if strings.Contains(err.Error(), "SYNTHETIC") || strings.Contains(err.Error(), "https:") {
				t.Fatal("secret error exposed")
			}
		})
	}
}

func TestRelayNeverFollowsRedirects(t *testing.T) {
	calls := 0
	c := &RelayClient{transportFor: func(RelayAccess) (http.RoundTripper, error) {
		return relayRoundTripper(func(*http.Request) (*http.Response, error) {
			calls++
			if calls > 1 {
				return &http.Response{StatusCode: 200, Body: io.NopCloser(strings.NewReader("ok")), Header: make(http.Header)}, nil
			}
			return &http.Response{StatusCode: 302, Body: io.NopCloser(strings.NewReader("")), Header: http.Header{"Location": []string{"https://evil.example/"}}}, nil
		}), nil
	}}
	_, err := c.Activate(context.Background(), RelayAccess{URL: relayFixtureURL, SourceIP: "192.0.2.2", PathConfirmed: true})
	if err == nil || calls != 1 {
		t.Fatalf("redirect followed: calls=%d err=%v", calls, err)
	}
}

func TestRelayObservationIsBoundedAndRejectsUnknownIP(t *testing.T) {
	for _, tc := range []struct {
		body  string
		valid bool
	}{
		{"198.51.100.7\n", true}, {"2001:db8::7", true}, {"", false}, {"garbage", false}, {"127.0.0.1", false}, {"192.168.1.2", false}, {"0.0.0.0", false}, {"fe80::1%eth0", false}, {strings.Repeat("x", 128), false},
	} {
		t.Run(tc.body[:min(len(tc.body), 20)], func(t *testing.T) {
			source := "192.0.2.2"
			if tc.body == "2001:db8::7" {
				source = "2001:db8::2"
			}
			c := &RelayClient{transportFor: func(a RelayAccess) (http.RoundTripper, error) {
				if a.SourceIP != source || a.Interface != "eth0" {
					t.Error("lost explicit source path")
				}
				return relayRoundTripper(func(r *http.Request) (*http.Response, error) {
					if r.URL.String() != "https://api64.ipify.org/" {
						t.Error("unapproved observation target")
					}
					return &http.Response{StatusCode: 200, Body: io.NopCloser(strings.NewReader(tc.body)), Header: make(http.Header)}, nil
				}), nil
			}}
			got, err := c.Observe(context.Background(), RelayAccess{URL: relayFixtureURL, SourceIP: source, Interface: "eth0", PathConfirmed: true})
			if tc.valid {
				if err != nil || got.PublicIP != strings.TrimSpace(tc.body) || got.ObservedAt.IsZero() {
					t.Fatalf("valid IP not observed: %+v %v", got, err)
				}
			} else if err == nil || got.PublicIP != "" {
				t.Error("unknown IP accepted")
			}
		})
	}
}

func TestRelayProductionTransportCannotIgnoreInterface(t *testing.T) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer ln.Close()
	tr, err := newRelayTransport(RelayAccess{Interface: "warpd-missing", PathConfirmed: true})
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	conn, err := tr.DialContext(ctx, "tcp", ln.Addr().String())
	if conn != nil {
		conn.Close()
	}
	if err == nil {
		t.Fatal("nonexistent interface silently fell back to ordinary route")
	}
	if tr.Proxy != nil || tr.MaxResponseHeaderBytes > relayHeaderLimit || tr.MaxResponseHeaderBytes == 0 || !tr.DisableCompression || tr.TLSClientConfig != nil && tr.TLSClientConfig.InsecureSkipVerify {
		t.Fatal("unsafe production transport")
	}
}

func TestRelayObservationRejectsWrongAddressFamilyAndCarrierNAT(t *testing.T) {
	for _, body := range []string{"2001:db8::7", "100.64.0.2", "240.0.0.2", "0.1.2.3"} {
		c := &RelayClient{transportFor: func(RelayAccess) (http.RoundTripper, error) {
			return relayRoundTripper(func(*http.Request) (*http.Response, error) {
				return &http.Response{StatusCode: 200, Body: io.NopCloser(strings.NewReader(body)), Header: make(http.Header)}, nil
			}), nil
		}}
		got, err := c.Observe(context.Background(), RelayAccess{URL: relayFixtureURL, SourceIP: "192.0.2.2", PathConfirmed: true})
		if err == nil || got.PublicIP != "" {
			t.Errorf("nonpublic or mismatched-family IP accepted: %s", body)
		}
	}
}
