package webcal

import (
	"context"
	"errors"
	"net/http"
	"os"
	"strings"
)

// FileTokenSource reads the current access token for every request so a token
// sidecar can rotate it without restarting webcal.
func FileTokenSource(path string) TokenSource {
	return func(_ context.Context, _ *http.Request) (string, error) {
		if path == "" {
			return "", errors.New("token file path is required")
		}
		data, err := os.ReadFile(path)
		if err != nil {
			return "", err
		}
		token := strings.TrimSpace(string(data))
		if token == "" {
			return "", errors.New("token file is empty")
		}
		return token, nil
	}
}
