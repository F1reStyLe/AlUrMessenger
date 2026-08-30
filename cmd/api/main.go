// Command api запускает HTTP-процесс; сборка зависимостей находится в internal/app.
package main

import (
	"os"

	"github.com/F1reStyLe/AlUrMessenger/internal/app"
	"github.com/F1reStyLe/AlUrMessenger/internal/platform/config"
)

// main передаёт ОС итоговый статус только после выполнения cleanup внутри app.Main:
// os.Exit сам по себе не выполняет отложенные вызовы.
func main() { os.Exit(app.Main(config.API)) }
