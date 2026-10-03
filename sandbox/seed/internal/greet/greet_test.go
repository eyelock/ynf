package greet

import "testing"

func TestGreet(t *testing.T) {
	if got := Greet("ynf", false); got != "Hello, ynf!" {
		t.Fatalf("got %q", got)
	}
	if got := Greet("ynf", true); got != "HELLO, YNF!" {
		t.Fatalf("got %q", got)
	}
}
