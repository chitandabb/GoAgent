package webresearch

import (
	"context"
	"errors"
	"net"
	"net/netip"
	"net/url"
	"regexp"
	"slices"
	"strings"
)

const maxPublicURLBytes = 2048

var (
	ErrUnsafePublicURL  = errors.New("public URL is not allowed")
	domainPattern       = regexp.MustCompile(`(?i)^(?:[a-z0-9](?:[a-z0-9-]{0,61}[a-z0-9])?\.)+[a-z0-9](?:[a-z0-9-]{0,61}[a-z0-9])?$`)
	blockedHostSuffixes = []string{
		".corp", ".home", ".internal", ".invalid", ".lan", ".local", ".localhost", ".test",
	}
	blockedAddressPrefixes = mustAddressPrefixes(
		"0.0.0.0/8", "10.0.0.0/8", "100.64.0.0/10", "127.0.0.0/8", "169.254.0.0/16",
		"172.16.0.0/12", "192.0.0.0/24", "192.0.2.0/24", "192.88.99.0/24", "192.168.0.0/16",
		"198.18.0.0/15", "198.51.100.0/24", "203.0.113.0/24", "224.0.0.0/4", "240.0.0.0/4",
		"::/128", "::1/128", "100::/64", "2001:db8::/32", "fc00::/7", "fe80::/10", "ff00::/8",
	)
)

type IPResolver interface {
	LookupNetIP(context.Context, string, string) ([]netip.Addr, error)
}

type URLPolicy struct {
	resolver                       IPResolver
	transparentEgressResolverCIDRs []netip.Prefix
}

type URLPolicyOption func(*URLPolicy)

var transparentEgressProxyRange = netip.MustParsePrefix("198.18.0.0/15")

// WithTransparentEgressResolverCIDRs permits only DNS answers in the explicit
// RFC 2544 benchmark range used by a trusted transparent egress proxy. It never
// permits URL literals or private, loopback, link-local, or multicast addresses.
func WithTransparentEgressResolverCIDRs(prefixes []netip.Prefix) URLPolicyOption {
	return func(policy *URLPolicy) {
		for _, prefix := range prefixes {
			prefix = prefix.Masked()
			if prefix.IsValid() && prefix.Bits() >= transparentEgressProxyRange.Bits() &&
				transparentEgressProxyRange.Contains(prefix.Addr()) {
				policy.transparentEgressResolverCIDRs = append(policy.transparentEgressResolverCIDRs, prefix)
			}
		}
	}
}

type PublicURL struct {
	value  string
	domain string
}

func (u PublicURL) String() string { return u.value }

func (u PublicURL) Domain() string { return u.domain }

func NewURLPolicy(resolver IPResolver, options ...URLPolicyOption) *URLPolicy {
	if resolver == nil {
		resolver = net.DefaultResolver
	}
	policy := &URLPolicy{resolver: resolver}
	for _, option := range options {
		if option != nil {
			option(policy)
		}
	}
	return policy
}

func (p *URLPolicy) Validate(ctx context.Context, raw string) (PublicURL, error) {
	if p == nil || p.resolver == nil || strings.TrimSpace(raw) == "" || len(raw) > maxPublicURLBytes {
		return PublicURL{}, ErrUnsafePublicURL
	}
	parsed, err := url.Parse(strings.TrimSpace(raw))
	if err != nil || parsed.Opaque != "" || parsed.Host == "" || parsed.User != nil {
		return PublicURL{}, ErrUnsafePublicURL
	}
	parsed.Scheme = strings.ToLower(parsed.Scheme)
	if parsed.Scheme != "http" && parsed.Scheme != "https" {
		return PublicURL{}, ErrUnsafePublicURL
	}
	host := strings.ToLower(strings.TrimSuffix(parsed.Hostname(), "."))
	if host == "" || host == "localhost" || strings.Contains(host, "%") {
		return PublicURL{}, ErrUnsafePublicURL
	}
	for _, suffix := range blockedHostSuffixes {
		if strings.HasSuffix(host, suffix) {
			return PublicURL{}, ErrUnsafePublicURL
		}
	}
	port := parsed.Port()
	if port != "" && !((parsed.Scheme == "http" && port == "80") || (parsed.Scheme == "https" && port == "443")) {
		return PublicURL{}, ErrUnsafePublicURL
	}
	address, addressErr := netip.ParseAddr(host)
	if addressErr == nil {
		if !publicAddress(address) {
			return PublicURL{}, ErrUnsafePublicURL
		}
	} else {
		if !domainPattern.MatchString(host) {
			return PublicURL{}, ErrUnsafePublicURL
		}
		addresses, lookupErr := p.resolver.LookupNetIP(ctx, "ip", host)
		if lookupErr != nil || len(addresses) == 0 {
			return PublicURL{}, ErrUnsafePublicURL
		}
		for _, address := range addresses {
			if !p.publicResolvedAddress(address) {
				return PublicURL{}, ErrUnsafePublicURL
			}
		}
	}
	parsed.Host = host
	if addressErr == nil && address.Is6() && port == "" {
		parsed.Host = "[" + host + "]"
	} else if port != "" {
		parsed.Host = net.JoinHostPort(host, port)
	}
	parsed.Fragment = ""
	parsed.RawFragment = ""
	return PublicURL{value: parsed.String(), domain: host}, nil
}

func (p *URLPolicy) publicResolvedAddress(address netip.Addr) bool {
	if publicAddress(address) {
		return true
	}
	address = address.Unmap()
	if !address.IsValid() || !address.IsGlobalUnicast() || address.IsPrivate() || address.IsLoopback() ||
		address.IsLinkLocalUnicast() || address.IsUnspecified() || address.IsMulticast() {
		return false
	}
	for _, prefix := range p.transparentEgressResolverCIDRs {
		if prefix.Contains(address) {
			return true
		}
	}
	return false
}

func publicAddress(address netip.Addr) bool {
	if !address.IsValid() {
		return false
	}
	address = address.Unmap()
	if !address.IsGlobalUnicast() || address.IsPrivate() || address.IsLoopback() ||
		address.IsLinkLocalUnicast() || address.IsUnspecified() || address.IsMulticast() {
		return false
	}
	for _, prefix := range blockedAddressPrefixes {
		if prefix.Contains(address) {
			return false
		}
	}
	return true
}

func mustAddressPrefixes(values ...string) []netip.Prefix {
	result := make([]netip.Prefix, 0, len(values))
	for _, value := range values {
		result = append(result, netip.MustParsePrefix(value))
	}
	return result
}

func normalizeDomainRules(values []string) ([]string, error) {
	if len(values) > 128 {
		return nil, errors.New("too many domain rules")
	}
	result := make([]string, 0, len(values))
	for _, raw := range values {
		domain := strings.ToLower(strings.TrimSuffix(strings.TrimSpace(raw), "."))
		if !domainPattern.MatchString(domain) || net.ParseIP(domain) != nil {
			return nil, ErrUnsafePublicURL
		}
		if !slices.Contains(result, domain) {
			result = append(result, domain)
		}
	}
	slices.Sort(result)
	return result, nil
}

func domainMatchesAny(domain string, rules []string) bool {
	domain = strings.ToLower(strings.TrimSuffix(strings.TrimSpace(domain), "."))
	for _, rule := range rules {
		if domain == rule || strings.HasSuffix(domain, "."+rule) {
			return true
		}
	}
	return false
}

func domainMatchesAnyRule(domain string, rules []string) bool {
	for _, rule := range rules {
		if domainMatchesAny(rule, []string{domain}) {
			return true
		}
	}
	return false
}
