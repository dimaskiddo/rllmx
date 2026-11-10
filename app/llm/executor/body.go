package executor

import "github.com/tidwall/sjson"

// SetRaw writes a raw JSON value and keeps the body when the write fails.
func SetRaw(body []byte, path, raw string) []byte {
	if next, err := sjson.SetRawBytes(body, path, []byte(raw)); err == nil {
		return next
	}
	return body
}

func SetValue(body []byte, path string, v any) []byte {
	if next, err := sjson.SetBytes(body, path, v); err == nil {
		return next
	}
	return body
}

func Remove(body []byte, path string) []byte {
	if next, err := sjson.DeleteBytes(body, path); err == nil {
		return next
	}
	return body
}
