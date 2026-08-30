// Command healthcheck позволяет distroless images проверять HTTP без curl/shell.
package main

import (
	"net/http"
	"os"
	"time"
)

// main ограничивает длительность probe и не выводит body/URL, содержащие данные.
func main() {
	url := "http://127.0.0.1:8080/health/ready"
	if len(os.Args) == 2 {
		url = os.Args[1]
	}
	client := http.Client{Timeout: 2 * time.Second}
	r, err := client.Get(url)
	if err != nil {
		os.Exit(1)
	}
	r.Body.Close()
	if r.StatusCode != http.StatusOK {
		os.Exit(1)
	}
}
