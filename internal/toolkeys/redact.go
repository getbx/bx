package toolkeys

import (
	"bytes"
	"encoding/json"
	"strings"
)

var sensitiveJSONFields = map[string]struct{}{
	"token": {}, "api_key": {}, "secret": {}, "password": {}, "private_key": {},
	"client_secret": {}, "access_token": {}, "refresh_token": {},
}

func RedactResponse(body []byte, contentType string, secrets []string) ([]byte, error) {
	for _, secret := range secrets {
		if secret != "" {
			body = bytes.ReplaceAll(body, []byte(secret), []byte("<redacted>"))
		}
	}
	if !strings.Contains(strings.ToLower(contentType), "json") {
		return body, nil
	}
	var value any
	if err := json.Unmarshal(body, &value); err != nil {
		return body, nil
	}
	redactJSON(value)
	return json.Marshal(value)
}

func redactJSON(value any) {
	switch v := value.(type) {
	case map[string]any:
		for key, child := range v {
			if _, ok := sensitiveJSONFields[strings.ToLower(key)]; ok {
				v[key] = "<redacted>"
			} else {
				redactJSON(child)
			}
		}
	case []any:
		for _, child := range v {
			redactJSON(child)
		}
	}
}
