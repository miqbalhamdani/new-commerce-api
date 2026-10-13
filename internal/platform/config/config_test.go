package config

import "testing"

func TestTrustedProxies(t *testing.T) {
	t.Setenv("TRUSTED_PROXIES", " 172.16.0.0/12, 10.1.2.3/8 ")
	got, err := TrustedProxies()
	if err != nil || len(got) != 2 || got[0].String() != "172.16.0.0/12" || got[1].String() != "10.0.0.0/8" {
		t.Errorf("TrustedProxies = %v, %v", got, err)
	}
	t.Setenv("TRUSTED_PROXIES", "")
	if got, err := TrustedProxies(); err != nil || len(got) != 0 {
		t.Errorf("unset: %v, %v", got, err)
	}
	t.Setenv("TRUSTED_PROXIES", "caddy")
	if _, err := TrustedProxies(); err == nil {
		t.Error("a name, not a CIDR, was accepted")
	}
}
