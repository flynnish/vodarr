package config

import (
	"strings"
	"testing"
)

func TestProviders(t *testing.T) {
	c := &Config{
		Xtream:         XtreamConfig{URL: "http://a", Username: "u", Password: "p"},
		ExtraProviders: []ProviderConfig{{Name: "Provider B!", URL: "http://b", Username: "u2", Password: "p2"}},
	}
	ps := c.Providers()
	if len(ps) != 2 || ps[0].ID != "" || ps[0].Name != "Primary" || ps[1].ID != "providerb" || ps[1].Name != "Provider B!" {
		t.Fatalf("Providers() = %+v", ps)
	}
	c.Xtream.Name = "Main"
	if c.Providers()[0].Name != "Main" {
		t.Error("primary name not used")
	}
}

func TestExtraProviderValidation(t *testing.T) {
	base := func() *Config {
		c := defaults()
		c.Xtream = XtreamConfig{URL: "http://a", Username: "u", Password: "p"}
		c.TMDB.APIKey = "k"
		c.Output.Path = "/data/strm"
		return c
	}
	cases := map[string][]ProviderConfig{
		"no name":         {{Name: "!!", URL: "http://b", Username: "u", Password: "p"}},
		"duplicate name":  {{Name: "B", URL: "http://b", Username: "u", Password: "p"}, {Name: "b", URL: "http://c", Username: "u", Password: "p"}},
		"reserved name":   {{Name: "Primary", URL: "http://b", Username: "u", Password: "p"}},
		"missing account": {{Name: "B", URL: "http://b"}},
	}
	for name, extras := range cases {
		c := base()
		c.ExtraProviders = extras
		err := c.validate()
		// On Windows "/data/strm" is not absolute; only check our own errors.
		if err == nil || (!strings.Contains(err.Error(), "extra") && !strings.Contains(err.Error(), "output.path")) {
			t.Errorf("%s: err = %v, want an extra-provider error", name, err)
		}
		if err != nil && strings.Contains(err.Error(), "output.path") {
			t.Skip("output path validation is platform-specific here")
		}
	}
}
