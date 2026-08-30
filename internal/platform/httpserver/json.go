package httpserver

import (
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"mime"
	"net/http"
	"strconv"
)

// errorEnvelope задаёт единую форму ошибок transport независимо от их источника.
type errorEnvelope struct {
	Error errorBody `json:"error"`
}

// errorBody содержит публичный код, безопасное пояснение и ID для сопоставления с логом.
// Внутренние errors и пользовательский payload сюда не передаются.
type errorBody struct {
	Code      string `json:"code"`
	Message   string `json:"message"`
	RequestID string `json:"request_id"`
}

// WriteError only accepts public codes/messages, never an infrastructure error.
func WriteError(w http.ResponseWriter, r *http.Request, status int, code, message string) {
	WriteJSON(w, r, status, errorEnvelope{Error: errorBody{code, message, RequestID(r.Context())}})
}

// WriteJSON сериализует ответ до отправки headers, чтобы ошибку кодирования можно
// было заменить безопасным 500. Вызывать только до фиксации ответа другим handler.
// HEAD сохраняет headers и длину соответствующего GET, но не отправляет тело.
func WriteJSON(w http.ResponseWriter, r *http.Request, status int, value any) {
	data, err := json.Marshal(value)
	if err != nil {
		status = http.StatusInternalServerError
		data, _ = json.Marshal(errorEnvelope{Error: errorBody{
			Code: "INTERNAL_ERROR", Message: "Internal server error", RequestID: RequestID(r.Context()),
		}})
	}
	data = append(data, '\n')
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.Header().Set("Content-Length", strconv.Itoa(len(data)))
	w.WriteHeader(status)
	if r.Method != http.MethodHead {
		// После WriteHeader заменить ответ ошибкой уже нельзя; разрыв соединения
		// обрабатывает transport, повторно писать JSON в этот поток не следует.
		_, _ = w.Write(data)
	}
}

// ReadJSON reads exactly one JSON object and rejects unknown fields. The server
// middleware enforces the body bound even for chunked requests.
// destination должен быть указателем на DTO. false означает, что ошибка уже записана:
// вызывающий handler обязан сразу завершиться, не выполняя бизнес-операцию.
func ReadJSON(w http.ResponseWriter, r *http.Request, destination any) bool {
	mediaType, _, err := mime.ParseMediaType(r.Header.Get("Content-Type"))
	if err != nil || mediaType != "application/json" {
		WriteError(w, r, http.StatusUnsupportedMediaType, "UNSUPPORTED_MEDIA", "Content-Type must be application/json")
		return false
	}
	if r.Body == nil {
		WriteError(w, r, http.StatusBadRequest, "INVALID_REQUEST", "A JSON object is required")
		return false
	}
	decoder := json.NewDecoder(r.Body)
	var raw json.RawMessage
	if err := decoder.Decode(&raw); err != nil {
		writeDecodeError(w, r, err)
		return false
	}
	var extra json.RawMessage
	// Проверяем EOF, включая завершающие пробелы: иначе второй JSON или превышение
	// лимита после первого объекта могли бы остаться незамеченными.
	if err := decoder.Decode(&extra); err != io.EOF {
		writeDecodeError(w, r, err)
		return false
	}
	raw = bytes.TrimSpace(raw)
	// JSON null/массив/скаляр не являются DTO-объектом даже при успешном Decode.
	if len(raw) == 0 || raw[0] != '{' {
		WriteError(w, r, http.StatusBadRequest, "INVALID_REQUEST", "A JSON object is required")
		return false
	}
	strict := json.NewDecoder(bytes.NewReader(raw))
	strict.DisallowUnknownFields()
	if err := strict.Decode(destination); err != nil {
		writeDecodeError(w, r, err)
		return false
	}
	return true
}

// writeDecodeError различает превышение размера и остальные ошибки формата.
// err может быть nil при успешно прочитанном втором объекте; это всё равно 400.
// Тексты decoder errors не публикуются, поскольку могут включать входные данные.
func writeDecodeError(w http.ResponseWriter, r *http.Request, err error) {
	var tooLarge *http.MaxBytesError
	if errors.As(err, &tooLarge) {
		WriteError(w, r, http.StatusRequestEntityTooLarge, "PAYLOAD_TOO_LARGE", "Request body exceeds the configured limit")
		return
	}
	WriteError(w, r, http.StatusBadRequest, "INVALID_REQUEST", "Request must contain one valid JSON object with known fields")
}
