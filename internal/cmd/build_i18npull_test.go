package cmd

import (
	"reflect"
	"testing"
)

// TestI18nPullBuildComposeRoundTrip guards the Unit 77 composer against the
// Unit 76 parser: the args runI18nPullBuild composes (multi-module positionals
// + baked --lang= / --from=) must re-parse to the same modules, language, and
// target. This ties the build-mode/sequence output to the direct command.
func TestI18nPullBuildComposeRoundTrip(t *testing.T) {
	args := composeArgs(
		[]string{"sale", "account"},
		[]chosenFlag{
			{name: "--lang", value: "es_MX", sep: "="},
			{name: "--from", value: "prod", sep: "="},
		},
	)
	want := []string{"sale", "account", "--lang=es_MX", "--from=prod"}
	if !reflect.DeepEqual(args, want) {
		t.Fatalf("composeArgs = %#v, want %#v", args, want)
	}

	p, err := parseI18nPullArgs(args)
	if err != nil {
		t.Fatalf("parseI18nPullArgs: %v", err)
	}
	if !reflect.DeepEqual(p.modules, []string{"sale", "account"}) ||
		p.lang != "es_MX" || p.from != "prod" {
		t.Errorf("round-trip = %+v, want modules=[sale account] lang=es_MX from=prod", p)
	}
}

// TestI18nPullBuildComposeSingleNoFrom covers a single module with the
// project's own [connect] (no --from baked) — still a valid round-trip.
func TestI18nPullBuildComposeSingleNoFrom(t *testing.T) {
	args := composeArgs(
		[]string{"sale"},
		[]chosenFlag{{name: "--lang", value: "fr_FR", sep: "="}},
	)
	p, err := parseI18nPullArgs(args)
	if err != nil {
		t.Fatalf("parseI18nPullArgs: %v", err)
	}
	if !reflect.DeepEqual(p.modules, []string{"sale"}) || p.lang != "fr_FR" || p.from != "" {
		t.Errorf("round-trip = %+v, want modules=[sale] lang=fr_FR from=", p)
	}
}
