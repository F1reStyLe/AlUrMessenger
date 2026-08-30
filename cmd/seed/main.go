// Command seed inserts repeatable development identities, never production data.
package main

import (
	"github.com/F1reStyLe/AlUrMessenger/internal/app"
	"os"
)

// main rejects non-development environments before database access.
func main() { os.Exit(app.ProvisionMain("seed", os.Args[1:])) }
