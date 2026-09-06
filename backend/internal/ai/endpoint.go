package ai

import (
	"context"
	"net"
	"net/netip"
	"net/url"
	"strings"
	"time"
)

// EndpointError says why an endpoint was refused, in words for the Owner.
type EndpointError struct{ Reason string }

func (e *EndpointError) Error() string { return "endpoint not allowed: " + e.Reason }

// lookupNetIP resolves a host; tests substitute it.
var lookupNetIP = func(ctx context.Context, host string) ([]netip.Addr, error) {
	return net.DefaultResolver.LookupNetIP(ctx, "ip", host)
}

// Ranges no provider lives in, beyond what netip already names: carrier-grade
// NAT and the "this network" block.
var refusedPrefixes = []netip.Prefix{
	netip.MustParsePrefix("100.64.0.0/10"),
	netip.MustParsePrefix("0.0.0.0/8"),
}

// ValidateEndpoint applies ADR 0010's rule for the dangerous field: https
// only, no credentials, a host that resolves, and no private, loopback,
// link-local or unspecified address among the answers. Its failure mode is
// a mistyped host receiving a live key, so it is checked on save, with the
// Owner present to be told. The value returned is what to store.
func ValidateEndpoint(ctx context.Context, raw string) (string, error) {
	raw = strings.TrimSpace(raw)
	u, err := url.Parse(raw)
	if err != nil || u.Scheme != "https" || u.Host == "" {
		return "", &EndpointError{"must be an https URL with a host"}
	}
	if u.User != nil {
		return "", &EndpointError{"must not carry credentials"}
	}
	if u.RawQuery != "" || u.Fragment != "" {
		return "", &EndpointError{"must not carry a query or fragment"}
	}
	host := strings.ToLower(u.Hostname())
	if host == "localhost" || strings.HasSuffix(host, ".localhost") || strings.HasSuffix(host, ".local") ||
		strings.HasSuffix(host, ".internal") || strings.HasSuffix(host, ".home.arpa") || !strings.Contains(host, ".") {
		if _, err := netip.ParseAddr(host); err != nil {
			return "", &EndpointError{"names a local host"}
		}
	}
	var addrs []netip.Addr
	if ip, err := netip.ParseAddr(host); err == nil {
		addrs = []netip.Addr{ip}
	} else {
		ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
		defer cancel()
		addrs, err = lookupNetIP(ctx, host)
		if err != nil || len(addrs) == 0 {
			return "", &EndpointError{"host does not resolve"}
		}
	}
	for _, a := range addrs {
		if refused(a.Unmap()) {
			return "", &EndpointError{"resolves to a private, loopback or link-local address"}
		}
	}
	u.Host = strings.ToLower(u.Host)
	u.Path = strings.TrimRight(u.Path, "/")
	return u.String(), nil
}

func refused(a netip.Addr) bool {
	if a.IsLoopback() || a.IsPrivate() || a.IsLinkLocalUnicast() || a.IsLinkLocalMulticast() ||
		a.IsInterfaceLocalMulticast() || a.IsMulticast() || a.IsUnspecified() {
		return true
	}
	for _, p := range refusedPrefixes {
		if p.Contains(a) {
			return true
		}
	}
	return false
}
