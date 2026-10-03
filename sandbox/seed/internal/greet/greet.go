// Package greet builds greetings.
package greet

import "strings"

// Greet returns "Hello, <name>!", in capitals when shout is true.
func Greet(name string, shout bool) string {
	g := "Hello, " + name + "!"
	if shout {
		return strings.ToUpper(g)
	}
	return g
}
