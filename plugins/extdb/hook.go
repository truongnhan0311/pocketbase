package extdb

import (
	"bytes"
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"time"
)

// HookPayload is POSTed to Config.Hook.URL after every successful update or delete.
type HookPayload struct {
	Event      string         `json:"event"` // "update" or "delete"
	Database   string         `json:"database"`
	Table      string         `json:"table"`
	PrimaryKey string         `json:"primaryKey"`
	ID         string         `json:"id"`
	Before     map[string]any `json:"before"`
	After      map[string]any `json:"after,omitempty"`
	Changes    map[string]any `json:"changes,omitempty"`
	Actor      HookActor      `json:"actor"`
	Timestamp  int64          `json:"timestamp"`
}

type HookActor struct {
	ID    string `json:"id"`
	Email string `json:"email"`
}

// HookResult is returned to the dashboard so a failed notification is visible to the superuser.
type HookResult struct {
	Sent     bool   `json:"sent"`
	Status   int    `json:"status,omitempty"`
	Error    string `json:"error,omitempty"`
	Response string `json:"response,omitempty"`
}

// Sign returns the hex HMAC-SHA256 of timestamp + "." + body, as sent in X-ExtDB-Signature.
func Sign(secret string, timestamp string, body []byte) string {
	mac := hmac.New(sha256.New, []byte(secret))
	mac.Write([]byte(timestamp))
	mac.Write([]byte("."))
	mac.Write(body)
	return hex.EncodeToString(mac.Sum(nil))
}

func sendHook(ctx context.Context, cfg HookConfig, payload HookPayload) HookResult {
	if cfg.URL == "" {
		return HookResult{}
	}

	body, err := json.Marshal(payload)
	if err != nil {
		return HookResult{Error: err.Error()}
	}

	ctx, cancel := context.WithTimeout(ctx, time.Duration(cfg.TimeoutSeconds)*time.Second)
	defer cancel()

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, cfg.URL, bytes.NewReader(body))
	if err != nil {
		return HookResult{Error: err.Error()}
	}

	timestamp := strconv.FormatInt(payload.Timestamp, 10)
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-ExtDB-Event", payload.Event)
	req.Header.Set("X-ExtDB-Timestamp", timestamp)
	if cfg.Secret != "" {
		req.Header.Set("X-ExtDB-Signature", Sign(cfg.Secret, timestamp, body))
	}
	for k, v := range cfg.Headers {
		req.Header.Set(k, v)
	}

	res, err := http.DefaultClient.Do(req)
	if err != nil {
		return HookResult{Sent: true, Error: err.Error()}
	}
	defer res.Body.Close()

	resBody, _ := io.ReadAll(io.LimitReader(res.Body, 2000))
	result := HookResult{Sent: true, Status: res.StatusCode, Response: string(resBody)}
	if res.StatusCode < 200 || res.StatusCode >= 300 {
		result.Error = fmt.Sprintf("hook responded with status %d", res.StatusCode)
	}
	return result
}
