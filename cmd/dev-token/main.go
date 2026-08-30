// Command dev-token prints a short-lived development JWT to its caller, not logs.
package main

import (
	"github.com/F1reStyLe/AlUrMessenger/internal/app"
	"os"
)

// main must never be used as a production authentication service.
func main() { os.Exit(app.ProvisionMain("dev-token", os.Args[1:])) }
