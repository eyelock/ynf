// Command greet prints a greeting.
package main

import (
	"flag"
	"fmt"

	"github.com/eyelock/ynf-sandbox/internal/greet"
)

func main() {
	shout := flag.Bool("shout", false, "print the greeting in capitals")
	name := flag.String("name", "world", "who to greet")
	flag.Parse()
	fmt.Println(greet.Greet(*name, *shout))
}
