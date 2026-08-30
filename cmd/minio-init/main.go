// Command minio-init повторяемо создаёт private bucket; runtime его не создаёт.
package main

import (
	"github.com/F1reStyLe/AlUrMessenger/internal/app"
	"os"
)

// main завершает процесс после bounded init и закрытия HTTP transport клиента.
func main() { os.Exit(app.AdminMain("minio-init", os.Args[1:])) }
