package httpkit

import (
	"net/http/httptest"
	"net/netip"
	"testing"
)

// TestClientIP: X-Real-IP is believed from the proxy in front and from no one
// else (04-api-spec.md §1, BR-014, BR-018).
func TestClientIP(t *testing.T) {
	t.Cleanup(func() { TrustedProxies = nil })
	for _, c := range []struct {
		name, peer, header string
		trusted            []netip.Prefix
		want               string
	}{
		{"no proxy trusted: the peer", "203.0.113.9:5000", "198.51.100.7", nil, "203.0.113.9"},
		{"from the proxy: its header", "172.18.0.5:5000", "198.51.100.7", []netip.Prefix{netip.MustParsePrefix("172.16.0.0/12")}, "198.51.100.7"},
		{"from anyone else: the peer", "203.0.113.9:5000", "198.51.100.7", []netip.Prefix{netip.MustParsePrefix("172.16.0.0/12")}, "203.0.113.9"},
		{"from the proxy, no header: the peer", "172.18.0.5:5000", "", []netip.Prefix{netip.MustParsePrefix("172.16.0.0/12")}, "172.18.0.5"},
		{"from the proxy, garbage: the peer", "172.18.0.5:5000", "1.2.3.4, 5.6.7.8", []netip.Prefix{netip.MustParsePrefix("172.16.0.0/12")}, "172.18.0.5"},
		{"IPv6 header", "[::ffff:172.18.0.5]:5000", "2001:db8::1", []netip.Prefix{netip.MustParsePrefix("172.16.0.0/12")}, "2001:db8::1"},
	} {
		t.Run(c.name, func(t *testing.T) {
			TrustedProxies = c.trusted
			r := httptest.NewRequest("GET", "/", nil)
			r.RemoteAddr = c.peer
			if c.header != "" {
				r.Header.Set("X-Real-IP", c.header)
			}
			if got := ClientIP(r); got.String() != c.want {
				t.Errorf("ClientIP = %s, want %s", got, c.want)
			}
		})
	}
}
