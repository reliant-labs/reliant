package netguard

import (
	"context"
	"errors"
	"fmt"
	"net"
	"net/http"
	"net/http/httptest"
	"net/netip"
	"net/url"
	"strconv"
	"strings"
	"sync/atomic"
	"syscall"
	"testing"
	"time"

	"golang.org/x/net/dns/dnsmessage"
)

type fakeResolver struct {
	answers map[string][]netip.Addr
	err     error
	calls   atomic.Int32
}

func (f *fakeResolver) LookupNetIP(_ context.Context, network, host string) ([]netip.Addr, error) {
	f.calls.Add(1)
	if network != "ip" {
		return nil, fmt.Errorf("fakeResolver: want network ip (A and AAAA), got %q", network)
	}
	if f.err != nil {
		return nil, f.err
	}
	if a, ok := f.answers[host]; ok {
		return a, nil
	}
	return nil, &net.DNSError{Err: "no such host", Name: host, IsNotFound: true}
}

func resolverOf(kv map[string][]string) *fakeResolver {
	f := &fakeResolver{answers: map[string][]netip.Addr{}}
	for h, ips := range kv {
		for _, s := range ips {
			f.answers[h] = append(f.answers[h], netip.MustParseAddr(s))
		}
	}
	return f
}

func TestCheckAddress_RangeTable(t *testing.T) {
	p := Policy{}
	for _, tc := range rangeCases {
		t.Run(tc.name, func(t *testing.T) {
			host := tc.addr
			if strings.Contains(host, ":") {
				host = "[" + host + "]"
			}
			err := p.CheckAddress(context.Background(), "https://"+host+":443/", nil)
			if tc.refused {
				if !errors.Is(err, ErrRefused) {
					t.Fatalf("CheckAddress(%s) = %v, want ErrRefused", tc.addr, err)
				}
			} else if err != nil {
				t.Fatalf("CheckAddress(%s) = %v, want nil", tc.addr, err)
			}
		})
	}
}

func TestCheckAddress_ResolvedAnswersTable(t *testing.T) {
	p := Policy{}
	for _, tc := range rangeCases {
		t.Run(tc.name, func(t *testing.T) {
			res := resolverOf(map[string][]string{"host.example.com": {tc.addr}})
			err := p.CheckAddress(context.Background(), "https://host.example.com/", res)
			if tc.refused != errors.Is(err, ErrRefused) {
				t.Fatalf("CheckAddress via %s = %v, want refused=%v", tc.addr, err, tc.refused)
			}
			if !tc.refused && err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
		})
	}
}

func TestCheckAddress_URLShape(t *testing.T) {
	public := resolverOf(map[string][]string{"api.example.com": {"93.184.216.34"}})
	cases := []struct {
		name    string
		policy  Policy
		url     string
		wantErr string // substring; "" means accepted
	}{
		{"https default port", Policy{}, "https://api.example.com/", ""},
		{"https explicit 443", Policy{}, "https://api.example.com:443/x?y=1#z", ""},
		{"uppercase scheme and host", Policy{}, "HTTPS://API.example.com/", ""},
		{"http refused", Policy{}, "http://api.example.com/", "https"},
		{"ftp refused", Policy{}, "ftp://api.example.com/", "https"},
		{"no scheme", Policy{}, "api.example.com", "https"},
		{"userinfo refused", Policy{}, "https://user:pw@api.example.com/", "userinfo"},
		{"empty userinfo refused", Policy{}, "https://@api.example.com/", "userinfo"},
		{"no host", Policy{}, "https:///path", "host"},
		{"opaque", Policy{}, "https:api.example.com", "host"},
		{"unparsable", Policy{}, "https://api.example.com:notaport/", ""},
		{"port outside default set", Policy{}, "https://api.example.com:6443/", "port"},
		{"port 80", Policy{}, "https://api.example.com:80/", "port"},
		{"caller port set admits 6443", Policy{Ports: []uint16{443, 6443}}, "https://api.example.com:6443/", ""},
		{"caller port set still refuses 8443", Policy{Ports: []uint16{443, 6443}}, "https://api.example.com:8443/", "port"},
		{"caller port set may drop 443", Policy{Ports: []uint16{6443}}, "https://api.example.com/", "port"},
		{"port zero", Policy{}, "https://api.example.com:0/", "port"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			err := tc.policy.CheckAddress(context.Background(), tc.url, public)
			switch {
			case tc.name == "unparsable":
				if err == nil {
					t.Fatal("want an error")
				}
			case tc.wantErr == "":
				if err != nil {
					t.Fatalf("want accepted, got %v", err)
				}
			default:
				if !errors.Is(err, ErrRefused) || !strings.Contains(err.Error(), tc.wantErr) {
					t.Fatalf("want ErrRefused mentioning %q, got %v", tc.wantErr, err)
				}
			}
		})
	}
}

func TestCheckAddress_AnyRefusedAnswerRefuses(t *testing.T) {
	res := resolverOf(map[string][]string{
		"mixed.example.com": {"93.184.216.34", "10.1.2.3"},
		"v6.example.com":    {"93.184.216.34", "fd00::1"},
		"ok.example.com":    {"93.184.216.34", "2606:4700:4700::1111"},
	})
	p := Policy{}
	for host, wantRefused := range map[string]bool{"mixed.example.com": true, "v6.example.com": true, "ok.example.com": false} {
		err := p.CheckAddress(context.Background(), "https://"+host+"/", res)
		if wantRefused != errors.Is(err, ErrRefused) {
			t.Errorf("%s: err = %v, want refused=%v", host, err, wantRefused)
		}
	}
}

func TestCheckAddress_ErrorNamesAddressAndRange(t *testing.T) {
	res := resolverOf(map[string][]string{"sneaky.example.com": {"169.254.169.254"}})
	err := Policy{}.CheckAddress(context.Background(), "https://sneaky.example.com/", res)
	if !errors.Is(err, ErrRefused) {
		t.Fatalf("err = %v", err)
	}
	for _, want := range []string{"sneaky.example.com", "169.254.169.254", "169.254.0.0/16"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("error %q does not name %q", err, want)
		}
	}
}

func TestCheckAddress_NXDOMAINAndNoAnswersAreRefused(t *testing.T) {
	res := resolverOf(nil)
	err := Policy{}.CheckAddress(context.Background(), "https://nope.example.com/", res)
	if !errors.Is(err, ErrRefused) {
		t.Fatalf("NXDOMAIN: err = %v, want ErrRefused", err)
	}
	empty := &fakeResolver{answers: map[string][]netip.Addr{"empty.example.com": {}}}
	err = Policy{}.CheckAddress(context.Background(), "https://empty.example.com/", empty)
	if !errors.Is(err, ErrRefused) {
		t.Fatalf("no answers: err = %v, want ErrRefused", err)
	}
	broken := &fakeResolver{err: errors.New("resolver down")}
	err = Policy{}.CheckAddress(context.Background(), "https://x.example.com/", broken)
	if !errors.Is(err, ErrRefused) {
		t.Fatalf("resolver failure: err = %v, want a refusal (fail closed)", err)
	}
}

func TestCheckAddress_WildcardDNSNamesAreRefused(t *testing.T) {
	res := resolverOf(map[string][]string{
		"10-0-0-1.nip.io":            {"10.0.0.1"},
		"169.254.169.254.nip.io":     {"169.254.169.254"},
		"7f000001.sslip.io":          {"127.0.0.1"},
		"metadata.attacker.test":     {"169.254.169.254"},
		"v6-loop.attacker.test":      {"::1"},
		"mapped.attacker.test":       {"::ffff:10.0.0.1"},
		"sixtofour.attacker.test":    {"2002:0a00:0001::1"},
		"nat64.attacker.test":        {"64:ff9b::10.0.0.1"},
		"pga.attacker.test":          {"199.36.153.4"},
		"cgnat.attacker.test":        {"100.64.0.1"},
		"link-local-v6.attacker.net": {"fe80::1"},
	})
	for host := range res.answers {
		err := Policy{}.CheckAddress(context.Background(), "https://"+host+"/", res)
		if !errors.Is(err, ErrRefused) {
			t.Errorf("%s: err = %v, want ErrRefused", host, err)
		}
	}
}

func TestCheckAddress_HostnameRulesRunBeforeResolution(t *testing.T) {
	res := resolverOf(map[string][]string{
		"localhost":                {"93.184.216.34"},
		"metadata.google.internal": {"93.184.216.34"},
		"kubernetes.default.svc":   {"93.184.216.34"},
		"a.svc.cluster.local":      {"93.184.216.34"},
		"printer.local":            {"93.184.216.34"},
		"foo.localhost":            {"93.184.216.34"},
		"kubernetes":               {"93.184.216.34"},
		"2130706433":               {"93.184.216.34"},
	})
	for host := range res.answers {
		res.calls.Store(0)
		err := Policy{}.CheckAddress(context.Background(), "https://"+host+"/", res)
		if !errors.Is(err, ErrRefused) {
			t.Errorf("%s: err = %v, want ErrRefused", host, err)
		}
		if res.calls.Load() != 0 {
			t.Errorf("%s: resolver consulted (%d calls) for a name the hostname rules refuse", host, res.calls.Load())
		}
	}
}

func TestCheckAddress_NilResolverUsesSystemResolverForLiteralsOnly(t *testing.T) {
	// A literal IP never needs DNS, so a nil resolver is fine for it.
	if err := (Policy{}).CheckAddress(context.Background(), "https://93.184.216.34/", nil); err != nil {
		t.Fatalf("public literal: %v", err)
	}
	if err := (Policy{}).CheckAddress(context.Background(), "https://10.0.0.1/", nil); !errors.Is(err, ErrRefused) {
		t.Fatalf("private literal: %v", err)
	}
}

func TestCheckAddress_ResolutionHonoursContext(t *testing.T) {
	slow := blockingResolver{}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	err := Policy{}.CheckAddress(ctx, "https://slow.example.com/", slow)
	if !errors.Is(err, ErrRefused) {
		t.Fatalf("err = %v, want a refusal on a cancelled context", err)
	}
}

type blockingResolver struct{}

func (blockingResolver) LookupNetIP(ctx context.Context, _, _ string) ([]netip.Addr, error) {
	<-ctx.Done()
	return nil, ctx.Err()
}

func TestAllow_AdmitsExactlyItsPrefixes(t *testing.T) {
	p := Policy{Allow: []netip.Prefix{
		netip.MustParsePrefix("10.20.0.0/16"),
		netip.MustParsePrefix("169.254.20.10/32"),
		netip.MustParsePrefix("fd12:3456::/32"),
	}}
	cases := []struct {
		addr    string
		refused bool
	}{
		{"10.20.0.1", false},
		{"10.20.255.254", false},
		{"169.254.20.10", false},
		{"fd12:3456::1", false},
		{"::ffff:10.20.1.1", false},
		// Neighbours stay refused.
		{"10.19.255.255", true},
		{"10.21.0.1", true},
		{"10.0.0.1", true},
		{"169.254.20.11", true},
		{"169.254.169.254", true},
		{"fd12:3457::1", true},
		{"127.0.0.1", true},
		{"::1", true},
	}
	for _, tc := range cases {
		reason, refused := p.Refused(netip.MustParseAddr(tc.addr))
		if refused != tc.refused {
			t.Errorf("Refused(%s) = %q, %v; want refused=%v", tc.addr, reason, refused, tc.refused)
		}
	}

	// The exception applies on the CheckAddress path too, and does not widen
	// the hostname rules.
	res := resolverOf(map[string][]string{
		"platform.example.com": {"10.20.1.1"},
		"other.example.com":    {"10.21.1.1"},
	})
	if err := p.CheckAddress(context.Background(), "https://platform.example.com/", res); err != nil {
		t.Errorf("allowed platform address refused: %v", err)
	}
	if err := p.CheckAddress(context.Background(), "https://other.example.com/", res); !errors.Is(err, ErrRefused) {
		t.Errorf("non-allowed private address accepted: %v", err)
	}
	if err := p.CheckAddress(context.Background(), "https://localhost/", res); !errors.Is(err, ErrRefused) {
		t.Errorf("Allow must not exempt hostname rules: %v", err)
	}
}

func TestAllow_ZeroPolicyAdmitsNothingPrivate(t *testing.T) {
	var p Policy
	if _, refused := p.Refused(netip.MustParseAddr("10.0.0.1")); !refused {
		t.Fatal("zero Policy must refuse 10.0.0.1")
	}
}

// control is the hook installed on the dialer: it sees the exact ip:port the
// socket is about to connect to.
func TestControl_RangeTable(t *testing.T) {
	p := Policy{}
	for _, tc := range rangeCases {
		t.Run(tc.name, func(t *testing.T) {
			err := p.control("tcp", net.JoinHostPort(tc.addr, "443"), nil)
			if tc.refused != errors.Is(err, ErrRefused) {
				t.Fatalf("control(%s) = %v, want refused=%v", tc.addr, err, tc.refused)
			}
			if !tc.refused && err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
		})
	}
}

func TestControl_EnforcesPorts(t *testing.T) {
	p := Policy{Ports: []uint16{443, 6443}}
	for addr, wantErr := range map[string]bool{
		"93.184.216.34:443":          false,
		"93.184.216.34:6443":         false,
		"93.184.216.34:22":           true,
		"93.184.216.34:5432":         true,
		"[2606:4700:4700::1111]:443": false,
		"[2606:4700:4700::1111]:80":  true,
	} {
		err := p.control("tcp", addr, nil)
		if wantErr != errors.Is(err, ErrRefused) {
			t.Errorf("control(%s) = %v, want refused=%v", addr, err, wantErr)
		}
	}
	if err := (Policy{}).control("tcp", "not-an-address", nil); !errors.Is(err, ErrRefused) {
		t.Errorf("malformed address must be refused, got %v", err)
	}
	if err := (Policy{}).control("tcp", "example.com:443", nil); !errors.Is(err, ErrRefused) {
		t.Errorf("a non-IP at connect time must be refused, got %v", err)
	}
}

func listenerPort(t *testing.T, ln net.Listener) uint16 {
	t.Helper()
	_, ps, err := net.SplitHostPort(ln.Addr().String())
	if err != nil {
		t.Fatal(err)
	}
	n, err := strconv.Atoi(ps)
	if err != nil {
		t.Fatal(err)
	}
	return uint16(n)
}

func TestDialContext_RefusesRefusedLiteralWithoutConnecting(t *testing.T) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer ln.Close()
	var accepted atomic.Int32
	go func() {
		for {
			c, err := ln.Accept()
			if err != nil {
				return
			}
			accepted.Add(1)
			c.Close()
		}
	}()

	p := Policy{Ports: []uint16{listenerPort(t, ln)}}
	_, err = p.DialContext(nil)(context.Background(), "tcp", ln.Addr().String())
	if !errors.Is(err, ErrRefused) {
		t.Fatalf("dial to loopback = %v, want ErrRefused", err)
	}
	time.Sleep(20 * time.Millisecond)
	if accepted.Load() != 0 {
		t.Fatal("the listener saw a connection: the dial reached the socket layer")
	}

	// The same dial succeeds once Allow names the address, proving the refusal
	// above came from the guard and not from the environment.
	p.Allow = []netip.Prefix{netip.MustParsePrefix("127.0.0.1/32")}
	c, err := p.DialContext(nil)(context.Background(), "tcp", ln.Addr().String())
	if err != nil {
		t.Fatalf("allowed dial: %v", err)
	}
	c.Close()
}

func TestDialContext_AllowAdmitsOnlyItsPrefix(t *testing.T) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer ln.Close()
	go func() {
		for {
			c, err := ln.Accept()
			if err != nil {
				return
			}
			c.Close()
		}
	}()
	port := listenerPort(t, ln)
	// 127.0.0.2 is also loopback and reaches the same listener on Linux, but
	// is outside the allowed /32.
	p := Policy{Ports: []uint16{port}, Allow: []netip.Prefix{netip.MustParsePrefix("127.0.0.1/32")}}
	dial := p.DialContext(nil)
	if c, err := dial(context.Background(), "tcp", net.JoinHostPort("127.0.0.1", strconv.Itoa(int(port)))); err != nil {
		t.Fatalf("127.0.0.1: %v", err)
	} else {
		c.Close()
	}
	if _, err := dial(context.Background(), "tcp", net.JoinHostPort("127.0.0.2", strconv.Itoa(int(port)))); !errors.Is(err, ErrRefused) {
		t.Fatalf("127.0.0.2 = %v, want ErrRefused", err)
	}
}

func TestDialContext_KeepsTheCallersDialerControl(t *testing.T) {
	var sawPrev atomic.Int32
	base := &net.Dialer{Control: func(_, _ string, _ syscall.RawConn) error {
		sawPrev.Add(1)
		return errors.New("caller control ran")
	}}
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer ln.Close()
	p := Policy{Ports: []uint16{listenerPort(t, ln)}, Allow: []netip.Prefix{netip.MustParsePrefix("127.0.0.0/8")}}
	_, err = p.DialContext(base)(context.Background(), "tcp", ln.Addr().String())
	if err == nil || !strings.Contains(err.Error(), "caller control ran") || sawPrev.Load() != 1 {
		t.Fatalf("err = %v, calls = %d; the caller's Control must still run after the guard admits", err, sawPrev.Load())
	}
	if base.ControlContext != nil || base.Control == nil {
		t.Error("DialContext must not mutate the caller's dialer")
	}
}

// fakeDNS answers every A query with ip, so a name resolves at DIAL time to
// whatever the test chooses, independent of what CheckAddress was told.
func fakeDNS(t *testing.T, ip netip.Addr) *net.Resolver {
	t.Helper()
	pc, err := net.ListenPacket("udp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { pc.Close() })
	go func() {
		buf := make([]byte, 1500)
		for {
			n, from, err := pc.ReadFrom(buf)
			if err != nil {
				return
			}
			var m dnsmessage.Message
			if m.Unpack(buf[:n]) != nil || len(m.Questions) == 0 {
				continue
			}
			q := m.Questions[0]
			resp := dnsmessage.Message{
				Header:    dnsmessage.Header{ID: m.ID, Response: true, Authoritative: true, RecursionAvailable: true},
				Questions: m.Questions,
			}
			if q.Type == dnsmessage.TypeA && ip.Is4() {
				resp.Answers = []dnsmessage.Resource{{
					Header: dnsmessage.ResourceHeader{Name: q.Name, Type: dnsmessage.TypeA, Class: dnsmessage.ClassINET, TTL: 1},
					Body:   &dnsmessage.AResource{A: ip.As4()},
				}}
			}
			out, err := resp.Pack()
			if err != nil {
				continue
			}
			pc.WriteTo(out, from)
		}
	}()
	return &net.Resolver{
		PreferGo: true,
		Dial: func(ctx context.Context, _, _ string) (net.Conn, error) {
			return (&net.Dialer{}).DialContext(ctx, "udp", pc.LocalAddr().String())
		},
	}
}

// DNS rebinding: CheckAddress is told the name is public and accepts it; by
// the time the dialer resolves, the name points at loopback. The dialer must
// refuse the connection, because it checks the address the socket connects to.
func TestDialContext_RefusesRebindingToRefusedIP(t *testing.T) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer ln.Close()
	var accepted atomic.Int32
	go func() {
		for {
			c, err := ln.Accept()
			if err != nil {
				return
			}
			accepted.Add(1)
			c.Close()
		}
	}()
	port := listenerPort(t, ln)
	p := Policy{Ports: []uint16{port}}

	check := resolverOf(map[string][]string{"rebind.example.test": {"93.184.216.34"}})
	if err := p.CheckAddress(context.Background(), fmt.Sprintf("https://rebind.example.test:%d/", port), check); err != nil {
		t.Fatalf("CheckAddress (public answer) = %v, want accepted", err)
	}

	base := &net.Dialer{Timeout: 3 * time.Second, Resolver: fakeDNS(t, netip.MustParseAddr("127.0.0.1"))}
	_, err = p.DialContext(base)(context.Background(), "tcp", fmt.Sprintf("rebind.example.test:%d", port))
	if !errors.Is(err, ErrRefused) {
		t.Fatalf("dial after rebinding = %v, want ErrRefused", err)
	}
	time.Sleep(20 * time.Millisecond)
	if accepted.Load() != 0 {
		t.Fatal("the listener saw a connection: rebinding reached the socket layer")
	}

	// Control: the same fake DNS, with the loopback address allowed, connects.
	p.Allow = []netip.Prefix{netip.MustParsePrefix("127.0.0.1/32")}
	c, err := p.DialContext(base)(context.Background(), "tcp", fmt.Sprintf("rebind.example.test:%d", port))
	if err != nil {
		t.Fatalf("control dial with Allow: %v", err)
	}
	c.Close()
}

func TestHTTPClient_Settings(t *testing.T) {
	c := HTTPClient(Policy{}, 7*time.Second)
	if c.Timeout != 7*time.Second {
		t.Errorf("Timeout = %v", c.Timeout)
	}
	tr, ok := c.Transport.(*http.Transport)
	if !ok {
		t.Fatalf("Transport = %T, want *http.Transport", c.Transport)
	}
	if tr.Proxy != nil {
		t.Error("Proxy must be nil: a proxy would be dialed instead of the destination")
	}
	if tr.DialContext == nil {
		t.Error("DialContext must be the guarded dialer")
	}
	if c.CheckRedirect == nil {
		t.Error("CheckRedirect must be set")
	}
}

func TestHTTPClient_RefusesLoopbackServer(t *testing.T) {
	var hits atomic.Int32
	srv := httptest.NewTLSServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) { hits.Add(1) }))
	defer srv.Close()
	u, _ := url.Parse(srv.URL)
	port, _ := strconv.Atoi(u.Port())

	c := HTTPClient(Policy{Ports: []uint16{uint16(port)}}, 2*time.Second)
	resp, err := c.Get(srv.URL)
	if resp != nil {
		resp.Body.Close()
	}
	if !errors.Is(err, ErrRefused) {
		t.Fatalf("err = %v, want ErrRefused", err)
	}
	if hits.Load() != 0 {
		t.Fatal("the server saw the request")
	}
}

func TestHTTPClient_RefusesEveryRedirect(t *testing.T) {
	var targetHits atomic.Int32
	target := httptest.NewTLSServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) { targetHits.Add(1) }))
	defer target.Close()
	redirector := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, target.URL, http.StatusFound)
	}))
	defer redirector.Close()

	ports := []uint16{}
	for _, s := range []*httptest.Server{target, redirector} {
		u, _ := url.Parse(s.URL)
		n, _ := strconv.Atoi(u.Port())
		ports = append(ports, uint16(n))
	}
	// Both hops are reachable under this policy (loopback allowed, both ports
	// allowed), so the only thing that can stop the second is the redirect rule.
	p := Policy{Ports: ports, Allow: []netip.Prefix{netip.MustParsePrefix("127.0.0.0/8")}}
	c := HTTPClient(p, 5*time.Second)
	tr := c.Transport.(*http.Transport)
	tr.TLSClientConfig = target.Client().Transport.(*http.Transport).TLSClientConfig

	resp, err := c.Get(redirector.URL)
	if resp != nil {
		resp.Body.Close()
	}
	if !errors.Is(err, ErrRedirectRefused) {
		t.Fatalf("err = %v, want ErrRedirectRefused", err)
	}
	if targetHits.Load() != 0 {
		t.Fatal("the redirect target saw a request")
	}

	// And the same client does reach a non-redirecting endpoint, so the refusal
	// above is the redirect rule, not a dead client.
	resp, err = c.Get(target.URL)
	if err != nil {
		t.Fatalf("direct request: %v", err)
	}
	resp.Body.Close()
	if targetHits.Load() != 1 {
		t.Fatalf("target hits = %d, want 1", targetHits.Load())
	}
}

func TestPolicyTransport_IsAFreshTransportEachCall(t *testing.T) {
	p := Policy{}
	if p.Transport() == p.Transport() {
		t.Error("Transport must return a new *http.Transport per call so callers can set TLSClientConfig")
	}
}
