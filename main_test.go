package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

var mockReceivedPath string

func startMock(t *testing.T) *http.Server {
	mux := http.NewServeMux()
	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		mockReceivedPath = r.URL.Path
		w.Header().Set("Content-Type", "text/event-stream")
		w.WriteHeader(200)
		flusher, _ := w.(http.Flusher)
		for i := 0; i < 3; i++ {
			fmt.Fprintf(w, "data: {\"choices\":[{\"delta\":{\"content\":\"chunk%d\"}}]}\n\n", i)
			if flusher != nil {
				flusher.Flush()
			}
			time.Sleep(30 * time.Millisecond)
		}
		fmt.Fprint(w, "data: [DONE]\n\n")
		if flusher != nil {
			flusher.Flush()
		}
	})
	srv := &http.Server{Addr: "127.0.0.1:59998", Handler: mux}
	go func() { _ = srv.ListenAndServe() }()
	time.Sleep(100 * time.Millisecond)
	return srv
}

func doGet(url, key string) (int, string) {
	req, _ := http.NewRequest("GET", url, nil)
	if key != "" {
		req.Header.Set("Authorization", "Bearer "+key)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return 0, err.Error()
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)
	return resp.StatusCode, string(body)
}

func doPost(url, key string, payload map[string]interface{}) (int, string) {
	data, _ := json.Marshal(payload)
	req, _ := http.NewRequest("POST", url, bytes.NewReader(data))
	req.Header.Set("Content-Type", "application/json")
	if key != "" {
		req.Header.Set("Authorization", "Bearer "+key)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return 0, err.Error()
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)
	return resp.StatusCode, string(body)
}

func TestEndToEnd(t *testing.T) {
	configDir = filepath.Join(os.TempDir(), "aigate_test")
	configPath = filepath.Join(configDir, "config.json")
	os.MkdirAll(configDir, 0755)

	cfg := Config{
		Server: ServerCfg{Host: "127.0.0.1", Port: 8318},
		APIKey: "test-key",
		Auth:   true,
		Routes: map[string]Route{
			"power-trio": {Models: map[string]ModelUpstream{
				"deepseek-chat": {BaseURL: "http://127.0.0.1:59998/v1", APIKey: "sk-fake", UpstreamModel: "deepseek-chat"},
				"glm-4.6":       {BaseURL: "http://127.0.0.1:59999/v1", APIKey: "sk-fake", UpstreamModel: "glm-4.6"},
				"gpt-4o":        {BaseURL: "http://127.0.0.1:59999/v1", APIKey: "sk-fake", UpstreamModel: "gpt-4o"},
			}},
			"solo": {Models: map[string]ModelUpstream{
				"only-one": {BaseURL: "http://127.0.0.1:59999/v1", APIKey: "sk-x", UpstreamModel: "only-one"},
			}},
		},
		ActiveRoute: "power-trio",
	}
	saveConfig(cfg)
	store.Reload()

	mockSrv := startMock(t)
	defer mockSrv.Close()

	aigateSrv := &http.Server{Addr: "127.0.0.1:8318", Handler: http.HandlerFunc(mainHandler)}
	go func() { _ = aigateSrv.ListenAndServe() }()
	defer aigateSrv.Close()
	time.Sleep(100 * time.Millisecond)

	BASE := "http://127.0.0.1:8318"
	KEY := "test-key"

	// 1. health
	s, _ := doGet(BASE+"/health", "")
	if s != 200 {
		t.Errorf("health: want 200, got %d", s)
	} else {
		t.Log("[PASS] health 200")
	}

	// 2. /v1/models 带key
	s, b := doGet(BASE+"/v1/models", KEY)
	if s != 200 {
		t.Errorf("models: want 200, got %d", s)
	}
	var ml struct {
		Data []map[string]string `json:"data"`
	}
	json.Unmarshal([]byte(b), &ml)
	ids := []string{}
	for _, m := range ml.Data {
		ids = append(ids, m["id"])
	}
	if strings.Join(ids, ",") != "deepseek-chat,glm-4.6,gpt-4o" {
		t.Errorf("models ids: want deepseek-chat,glm-4.6,gpt-4o, got %v", ids)
	} else {
		t.Log("[PASS] models 含 deepseek-chat/glm-4.6/gpt-4o")
	}

	// 3. /v1/models 无key
	s, _ = doGet(BASE+"/v1/models", "")
	if s != 401 {
		t.Errorf("no key: want 401, got %d", s)
	} else {
		t.Log("[PASS] no key 401")
	}

	// 4. unknown model
	s, _ = doPost(BASE+"/v1/chat/completions", KEY, map[string]interface{}{"model": "unknown", "messages": []interface{}{}})
	if s != 404 {
		t.Errorf("unknown model: want 404, got %d", s)
	} else {
		t.Log("[PASS] unknown model 404")
	}

	// 5. fake upstream 502
	s, _ = doPost(BASE+"/v1/chat/completions", KEY, map[string]interface{}{"model": "glm-4.6", "messages": []interface{}{map[string]string{"role": "user", "content": "hi"}}})
	if s != 502 {
		t.Errorf("fake upstream: want 502, got %d", s)
	} else {
		t.Log("[PASS] fake upstream 502")
	}

	// 6. 热重载：切到 solo
	cfg2 := store.Get()
	cfg2.ActiveRoute = "solo"
	saveConfig(cfg2)
	store.Reload()
	s, b = doGet(BASE+"/v1/models", KEY)
	json.Unmarshal([]byte(b), &ml)
	ids2 := []string{}
	for _, m := range ml.Data {
		ids2 = append(ids2, m["id"])
	}
	if strings.Join(ids2, ",") != "only-one" {
		t.Errorf("switch to solo: want [only-one], got %v", ids2)
	} else {
		t.Log("[PASS] 切换到 solo 后 models=['only-one']")
	}
	cfg2.ActiveRoute = "power-trio"
	saveConfig(cfg2)
	store.Reload()

	// 7. embeddings 502
	s, _ = doPost(BASE+"/v1/embeddings", KEY, map[string]interface{}{"model": "glm-4.6", "input": "hello"})
	if s != 502 {
		t.Errorf("embeddings: want 502, got %d", s)
	} else {
		t.Log("[PASS] embeddings fake 502")
	}

	// 8. 流式转发
	mockReceivedPath = ""
	s, b = doPost(BASE+"/v1/chat/completions", KEY, map[string]interface{}{"model": "deepseek-chat", "messages": []interface{}{map[string]string{"role": "user", "content": "hi"}}, "stream": true})
	if !strings.Contains(b, "data:") || !strings.Contains(b, "[DONE]") {
		t.Errorf("stream: no SSE data, got %s", b[:100])
	} else {
		t.Log("[PASS] 流式收到 SSE data 行")
	}
	if strings.Count(b, "chunk") < 3 {
		t.Errorf("stream: want >=3 chunks, got %d", strings.Count(b, "chunk"))
	} else {
		t.Log("[PASS] 流式透传 3 个 chunk")
	}

	// 9. URL 拼接
	if mockReceivedPath != "/v1/chat/completions" {
		t.Errorf("URL join: want /v1/chat/completions, got %s", mockReceivedPath)
	} else {
		t.Log("[PASS] URL 拼接无重复 /v1")
	}

	// 10. /v1/messages
	mockReceivedPath = ""
	s, _ = doPost(BASE+"/v1/messages", KEY, map[string]interface{}{"model": "deepseek-chat", "messages": []interface{}{map[string]string{"role": "user", "content": "hi"}}, "stream": true})
	if mockReceivedPath != "/v1/messages" {
		t.Errorf("/v1/messages: want /v1/messages, got %s", mockReceivedPath)
	} else {
		t.Log("[PASS] /v1/messages 转发到上游 /v1/messages")
	}
}
