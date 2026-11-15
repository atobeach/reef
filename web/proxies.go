package web

import (
	"net"
	"net/http"
	"net/netip"
	"strings"
)

// TrustedProxies decides whose forwarded headers to believe. The client IP
// it resolves keys every per-IP rate-limit bucket, so an untrusted peer's
// header must never move it; nil trusts none, and the client is the TCP peer.
type TrustedProxies struct {
	// peerOnly is "*": only the immediate peer is trusted, so the client is
	// the rightmost X-Forwarded-For entry.
	peerOnly bool
	prefixes []netip.Prefix
}

// ParseTrustedProxies accepts IPs/CIDRs, or "*" for the immediate peer (a
// load balancer). No entries trusts no proxy, and a malformed one is dropped,
// never trusted.
func ParseTrustedProxies(entries []string) *TrustedProxies {
	if len(entries) == 1 && entries[0] == "*" {
		return &TrustedProxies{peerOnly: true}
	}

	return &TrustedProxies{prefixes: ParsePrefixes(entries)}
}

// ParsePrefixes parses IPs and CIDRs, dropping anything malformed.
func ParsePrefixes(entries []string) []netip.Prefix {
	var prefixes []netip.Prefix
	for _, entry := range entries {
		if prefix, err := netip.ParsePrefix(entry); err == nil {
			prefixes = append(prefixes, prefix.Masked())
		} else if addr, err := netip.ParseAddr(entry); err == nil {
			prefixes = append(prefixes, netip.PrefixFrom(addr, addr.BitLen()))
		}
	}

	return prefixes
}

// ValidIP reports a bare v4/v6 address: no zone, no port.
func ValidIP(value string) bool {
	addr, err := netip.ParseAddr(value)

	return err == nil && addr.Zone() == ""
}

// Contains reports whether ip falls inside any of the prefixes.
func Contains(prefixes []netip.Prefix, ip string) bool {
	addr, err := netip.ParseAddr(ip)
	if err != nil {
		return false
	}

	addr = addr.Unmap()
	for _, prefix := range prefixes {
		if prefix.Contains(addr) {
			return true
		}
	}

	return false
}

func (p *TrustedProxies) trusts(ip string, peer string) bool {
	switch {
	case p == nil:
		return false
	case p.peerOnly:
		return ip == peer
	}

	return Contains(p.prefixes, ip)
}

func (p *TrustedProxies) clientIP(r *http.Request) string {
	peer := peerIP(r)
	if !p.trusts(peer, peer) {
		return peer
	}

	var chain []string
	for _, header := range r.Header.Values("X-Forwarded-For") {
		for entry := range strings.SplitSeq(header, ",") {
			if entry = strings.TrimSpace(entry); entry != "" {
				chain = append(chain, entry)
			}
		}
	}

	if len(chain) == 0 {
		return peer
	}

	// Walk from the nearest hop outwards; the first address we don't trust
	// is the client. Malformed entries mean the chain can't be believed.
	for i := len(chain) - 1; i >= 0; i-- {
		if !ValidIP(chain[i]) {
			return peer
		}

		if !p.trusts(chain[i], peer) {
			return chain[i]
		}
	}

	return chain[0]
}

func (p *TrustedProxies) isSecure(r *http.Request) bool {
	peer := peerIP(r)
	if proto := r.Header.Get("X-Forwarded-Proto"); proto != "" && p.trusts(peer, peer) {
		first, _, _ := strings.Cut(proto, ",")

		return strings.EqualFold(strings.TrimSpace(first), "https")
	}

	return r.TLS != nil
}

func peerIP(r *http.Request) string {
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		return r.RemoteAddr
	}

	return host
}
