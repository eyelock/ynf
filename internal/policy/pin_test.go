package policy_test

import (
	"strings"
	"testing"

	"github.com/eyelock/ynf/internal/policy"
)

func TestParsePin(t *testing.T) {
	for _, c := range []struct {
		in        string
		repo, ref string
		ok        bool
		err       string
	}{
		{in: "github.com/org/harness@v1.2.0", repo: "github.com/org/harness", ref: "v1.2.0", ok: true},
		{in: "https://github.com/org/harness.git@3f9c2e1", repo: "https://github.com/org/harness.git", ref: "3f9c2e1", ok: true},
		{in: "file:///srv/harness.git@v1", repo: "file:///srv/harness.git", ref: "v1", ok: true},
		{in: "git@github.com:org/harness@release/1.0", repo: "git@github.com:org/harness", ref: "release/1.0", ok: true},
		// Not pins: a folder, an installed id with or without a version, an address with no ref.
		{in: "."},
		{in: "tools/harness"},
		{in: "local/named"},
		{in: "local/named@1.0"},
		{in: "david@my-registry"},
		{in: "git@github.com:org/harness"},
		{in: "github.com/org/harness"},
		// Shaped like pins, and wrong.
		{in: "github.com/org/harness@", err: "pin it to a tag or commit"},
		{in: "github.com/org/harness@--force", err: "pin it to a tag or commit"},
		{in: "github.com/org/harness@v 1", err: "pin it to a tag or commit"},
		{in: "-x://y@v1", err: "looks like a flag"},
	} {
		p, ok, err := policy.ParsePin(c.in)
		switch {
		case c.err != "":
			if err == nil || !strings.Contains(err.Error(), c.err) {
				t.Errorf("%q: %v, want %q", c.in, err, c.err)
			}
		case err != nil || ok != c.ok || p.Repo != c.repo || p.Ref != c.ref:
			t.Errorf("%q: %+v %v %v, want %q %q %v", c.in, p, ok, err, c.repo, c.ref, c.ok)
		}
	}
}

func TestLoadRefusesAnUnusablePin(t *testing.T) {
	doc := `version: 1
lanes:
  a:
    kind: originate
    intake: [{github.search: "label:x", every: 5m}]
    run:
      runner: ynh
      executor: process
      ynh: {harness: "github.com/org/harness@"}
    when: {converged: open_pr}
`
	if _, err := policy.Load([]byte(doc)); err == nil || !strings.Contains(err.Error(), "lane a: run.ynh.harness") {
		t.Fatalf("%v", err)
	}
	good := strings.Replace(doc, `harness@"`, `harness@v1"`, 1)
	f, err := policy.Load([]byte(good))
	if err != nil {
		t.Fatal(err)
	}
	if p := f.Pins()["a"]; p.Repo != "github.com/org/harness" || p.Ref != "v1" || p.String() != "github.com/org/harness@v1" {
		t.Errorf("%+v", p)
	}
}
