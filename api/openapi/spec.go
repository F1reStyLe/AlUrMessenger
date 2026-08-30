// Package openapi embeds the reviewed HTTP contract in the same binary as its routes.
package openapi

import _ "embed"

// Document is served without filesystem lookup or remote specification downloads.
//
//go:embed openapi.json
var Document []byte
