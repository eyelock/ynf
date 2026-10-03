package report

import "testing"

func TestCountMatches(t *testing.T) {
	if got := CountMatches([]string{"a ynf", "b", "ynf c"}, "ynf"); got != 2 {
		t.Fatalf("got %d", got)
	}
}

func TestTitle(t *testing.T) {
	if got := Title([]string{"", " Report "}); got != "Report" {
		t.Fatalf("got %q", got)
	}
}
