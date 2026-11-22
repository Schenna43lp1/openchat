// Client addresses behind reverse proxies.
package main

import (
	"fmt"
	"net/http"
	"net/netip"
	"strings"
)

// parseTrustedProxies accepts a comma-separated list of addresses and networks.
func parseTrustedProxies(raw string) ([]netip.Prefix, error) {
	var trusted []netip.Prefix
	for part := range strings.SplitSeq(raw, ",") {
		part = strings.TrimSpace(part)
		if part == "" {
			continue
		}
		if addr, err := netip.ParseAddr(part); err == nil {
			addr = addr.Unmap()
			trusted = append(trusted, netip.PrefixFrom(addr, addr.BitLen()))
			continue
		}
		prefix, err := netip.ParsePrefix(part)
		if err != nil {
			return nil, fmt.Errorf("invalid proxy %q", part)
		}
		trusted = append(trusted, prefix.Masked())
	}
	return trusted, nil
}

// clientKey identifies a client for rate limiting. X-Forwarded-For is read from the right and only
// as far as it was written by trusted proxies, because everything to the left can be forged.
func clientKey(r *http.Request, trusted []netip.Prefix) string {
	peer, err := netip.ParseAddrPort(r.RemoteAddr)
	if err != nil {
		return r.RemoteAddr
	}
	client := peer.Addr().Unmap()
	hops := strings.Split(strings.Join(r.Header.Values("X-Forwarded-For"), ","), ",")
	for i := len(hops) - 1; i >= 0 && containsAddr(trusted, client); i-- {
		hop, err := netip.ParseAddr(strings.TrimSpace(hops[i]))
		if err != nil {
			break
		}
		client = hop.Unmap()
	}
	if client.Is6() {
		// One subscriber usually controls a whole /64.
		if network, err := client.WithZone("").Prefix(64); err == nil {
			return network.String()
		}
	}
	return client.String()
}

func containsAddr(prefixes []netip.Prefix, addr netip.Addr) bool {
	for _, prefix := range prefixes {
		if prefix.Contains(addr) {
			return true
		}
	}
	return false
}
