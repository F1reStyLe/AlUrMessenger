// Command migrate применяет embedded SQL под отдельной учётной записью с DDL правами.
package main

import (
	"github.com/F1reStyLe/AlUrMessenger/internal/app"
	"os"
)

// main отдаёт exit code только после закрытия migration connections и signal cleanup.
func main() { os.Exit(app.AdminMain("migrate", os.Args[1:])) }
