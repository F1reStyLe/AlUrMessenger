// Command user-role assigns a Project-local Chat role using operator credentials.
package main

import (
	"os"

	"github.com/F1reStyLe/AlUrMessenger/internal/app"
)

// main deliberately exposes no public role escalation endpoint.
func main() { os.Exit(app.ProvisionMain("user-role", os.Args[1:])) }
