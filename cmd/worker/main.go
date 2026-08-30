// Command worker запускает отдельный процесс фоновой роли.
// На этапе bootstrap доступны только probes; обработчики jobs ещё не подключены.
package main

import (
	"os"

	"github.com/F1reStyLe/AlUrMessenger/internal/app"
	"github.com/F1reStyLe/AlUrMessenger/internal/platform/config"
)

// main завершает процесс после возврата из app.Main, где выполнен lifecycle cleanup.
func main() { os.Exit(app.Main(config.Worker)) }
