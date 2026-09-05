// Package migrations встраивает SQL в бинарники: запуск не зависит от cwd или
// наличия исходников. Каталог содержит только настоящие миграции текущей фазы.
package migrations

import "embed"

// Files — неизменяемый набор goose SQL migrations, общий для migrate и schema check.
//
//go:embed *.sql
var Files embed.FS

// Version — ожидаемая версия схемы текущего бинарника; изменяется вместе с SQL.
const Version int64 = 10
