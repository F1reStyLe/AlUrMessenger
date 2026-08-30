// Command project provisions trusted Project bindings using operator credentials.
package main

import (
	"github.com/F1reStyLe/AlUrMessenger/internal/app"
	"os"
)

// main exposes no HTTP provisioning endpoint.
func main() { os.Exit(app.ProvisionMain("project", os.Args[1:])) }
