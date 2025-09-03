package websocket

import "encoding/json"

// Утилита для конвертации interface{} -> struct
func mapToStruct(data interface{}, target interface{}) error {
	jsonBytes, _ := json.Marshal(data)
	return json.Unmarshal(jsonBytes, target)
}

// Утилита для безопасного маршалинга
func mustMarshal(v Message) []byte {
	data, _ := json.Marshal(v)
	return data
}
