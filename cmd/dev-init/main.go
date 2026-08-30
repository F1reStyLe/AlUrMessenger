// Command dev-init создаёт локальные secrets перед первым development Compose запуском.
package main

import (
	"flag"
	"fmt"
	"github.com/F1reStyLe/AlUrMessenger/internal/devinit"
	"os"
)

// main не выводит credentials; directory нужен для изолированных стендов и tests.
func main() {
	directory := flag.String("dir", ".local", "development secret directory")
	flag.Parse()
	if flag.NArg() != 0 {
		os.Exit(1)
	}
	if err := devinit.Initialize(os.Getenv("APP_ENV"), *directory); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	fmt.Println("development secrets verified")
}
