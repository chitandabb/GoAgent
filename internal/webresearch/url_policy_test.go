package webresearch

import (
	"context"
	"net/netip"
	"testing"
)

func TestURLPolicyAllowsOnlyPublicHTTPSTargets(t *testing.T) {
	policy := NewURLPolicy(resolverStub{addresses: map[string][]netip.Addr{
		"public.example.org": {netip.MustParseAddr("8.8.8.8")},
		"mixed.example.org":  {netip.MustParseAddr("8.8.8.8"), netip.MustParseAddr("10.0.0.2")},
	}})
	tests := []struct {
		name    string
		value   string
		want    string
		wantErr bool
	}{
		{name: "public", value: "HTTPS://Public.Example.Org/guide?q=1#fragment", want: "https://public.example.org/guide?q=1"},
		{name: "public IP", value: "https://8.8.8.8/", want: "https://8.8.8.8/"},
		{name: "private literal", value: "http://10.0.0.1/", wantErr: true},
		{name: "metadata", value: "http://169.254.169.254/latest", wantErr: true},
		{name: "mixed DNS", value: "https://mixed.example.org/", wantErr: true},
		{name: "userinfo", value: "https://user:pass@public.example.org/", wantErr: true},
		{name: "nonstandard port", value: "https://public.example.org:8443/", wantErr: true},
		{name: "file", value: "file:///etc/passwd", wantErr: true},
		{name: "internal suffix", value: "https://service.internal/", wantErr: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := policy.Validate(context.Background(), tt.value)
			if (err != nil) != tt.wantErr {
				t.Fatalf("Validate() error=%v, wantErr=%v", err, tt.wantErr)
			}
			if err == nil && got.String() != tt.want {
				t.Fatalf("Validate()=%q, want %q", got.String(), tt.want)
			}
		})
	}
}
