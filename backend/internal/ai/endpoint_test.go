package ai

import (
	"context"
	"errors"
	"net/netip"
	"testing"
)

func TestValidateEndpoint(t *testing.T) {
	answers := map[string][]netip.Addr{
		"api.openai.com":   {netip.MustParseAddr("104.18.7.192")},
		"proxy.corp.test":  {netip.MustParseAddr("10.0.0.5")},
		"mixed.corp.test":  {netip.MustParseAddr("203.0.113.9"), netip.MustParseAddr("192.168.1.1")},
		"v6.provider.test": {netip.MustParseAddr("2606:4700::1")},
		"cgnat.test":       {netip.MustParseAddr("100.64.3.4")},
	}
	original := lookupNetIP
	lookupNetIP = func(_ context.Context, host string) ([]netip.Addr, error) {
		addrs, ok := answers[host]
		if !ok {
			return nil, errors.New("no such host")
		}
		return addrs, nil
	}
	t.Cleanup(func() { lookupNetIP = original })

	good := map[string]string{
		"https://api.openai.com/v1":   "https://api.openai.com/v1",
		"https://API.openai.com/v1/":  "https://api.openai.com/v1",
		"https://v6.provider.test/v1": "https://v6.provider.test/v1",
		"https://203.0.113.9/v1":      "https://203.0.113.9/v1",
	}
	for raw, want := range good {
		got, err := ValidateEndpoint(context.Background(), raw)
		if err != nil || got != want {
			t.Errorf("%s: got %q, %v", raw, got, err)
		}
	}
	bad := []string{
		"http://api.openai.com/v1",
		"https://user:pw@api.openai.com/v1",
		"https://api.openai.com/v1?x=1",
		"https://localhost/v1",
		"https://proxy/v1",
		"https://proxy.corp.test/v1",
		"https://mixed.corp.test/v1",
		"https://127.0.0.1/v1",
		"https://[::1]/v1",
		"https://169.254.169.254/latest",
		"https://cgnat.test/v1",
		"https://nowhere.test/v1",
		"https://[::ffff:10.0.0.1]/v1",
		"",
	}
	for _, raw := range bad {
		var ee *EndpointError
		if _, err := ValidateEndpoint(context.Background(), raw); !errors.As(err, &ee) {
			t.Errorf("%q accepted", raw)
		}
	}
}
