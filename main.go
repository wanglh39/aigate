package main

import (
	"bytes"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"log"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"
)

const (
	defaultHost = "127.0.0.1"
	defaultPort = 8317
)

// ------------------------------- 配置结构 -------------------------------

type ServerCfg struct {
	Host string `json:"host"`
	Port int    `json:"port"`
}

type ModelUpstream struct {
	BaseURL       string `json:"baseURL"`
	APIKey        string `json:"apiKey"`
	UpstreamModel string `json:"upstreamModel"`
}

type Route struct {
	Models map[string]ModelUpstream `json:"models"`
}

type Config struct {
	Server      ServerCfg        `json:"server"`
	APIKey      string           `json:"api_key"`
	Auth        bool             `json:"auth"`
	Routes      map[string]Route `json:"routes"`
	ActiveRoute string           `json:"active_route"`
}

var (
	configDir  string
	configPath string
	store      = &ConfigStore{}
)

func resolveConfigDir() string {
	if env := os.Getenv("AIGATE_HOME"); env != "" {
		return env
	}
	exe, err := os.Executable()
	if err != nil {
		return ".aigate"
	}
	return filepath.Join(filepath.Dir(exe), ".aigate")
}

func defaultConfig() Config {
	return Config{
		Server: ServerCfg{Host: defaultHost, Port: defaultPort},
		Auth:   true,
		Routes: map[string]Route{},
	}
}

func loadConfig() (Config, error) {
	data, err := os.ReadFile(configPath)
	if err != nil {
		if os.IsNotExist(err) {
			return defaultConfig(), nil
		}
		return Config{}, err
	}
	var cfg Config
	if err := json.Unmarshal(data, &cfg); err != nil {
		return Config{}, err
	}
	if cfg.Server.Host == "" {
		cfg.Server.Host = defaultHost
	}
	if cfg.Server.Port == 0 {
		cfg.Server.Port = defaultPort
	}
	if cfg.Routes == nil {
		cfg.Routes = map[string]Route{}
	}
	return cfg, nil
}

func saveConfig(cfg Config) error {
	if err := os.MkdirAll(configDir, 0755); err != nil {
		return err
	}
	data, err := json.MarshalIndent(cfg, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(configPath, data, 0644)
}

func activeModels(cfg Config) map[string]ModelUpstream {
	if cfg.ActiveRoute == "" {
		return nil
	}
	r, ok := cfg.Routes[cfg.ActiveRoute]
	if !ok {
		return nil
	}
	return r.Models
}

func modelNames(models map[string]ModelUpstream) []string {
	names := make([]string, 0, len(models))
	for k := range models {
		names = append(names, k)
	}
	sort.Strings(names)
	return names
}

// ------------------------------- 热重载 -------------------------------

type ConfigStore struct {
	cfg    Config
	mtime  time.Time
	loaded bool
	mu     sync.Mutex
}

func (s *ConfigStore) Get() Config {
	s.mu.Lock()
	defer s.mu.Unlock()
	var mt time.Time
	if st, err := os.Stat(configPath); err == nil {
		mt = st.ModTime()
	}
	if !s.loaded || !mt.Equal(s.mtime) {
		if c, err := loadConfig(); err == nil {
			s.cfg = c
			s.mtime = mt
			s.loaded = true
		}
	}
	return s.cfg
}

func (s *ConfigStore) Reload() {
	s.mu.Lock()
	defer s.mu.Unlock()
	if c, err := loadConfig(); err == nil {
		s.cfg = c
		if st, e := os.Stat(configPath); e == nil {
			s.mtime = st.ModTime()
		}
		s.loaded = true
	}
}

// ------------------------------- HTTP 工具 -------------------------------

func joinURL(base, path string) string {
	base = strings.TrimRight(base, "/")
	if strings.HasSuffix(base, "/v1") && strings.HasPrefix(path, "/v1/") {
		return base + path[3:]
	}
	return base + path
}

func sendJSON(w http.ResponseWriter, status int, obj interface{}) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(status)
	json.NewEncoder(w).Encode(obj)
}

func errObj(msg, typ string) map[string]interface{} {
	return map[string]interface{}{"error": map[string]string{"message": msg, "type": typ}}
}

func checkAuth(r *http.Request, cfg Config) bool {
	if !cfg.Auth || cfg.APIKey == "" {
		return true
	}
	auth := r.Header.Get("Authorization")
	if strings.HasPrefix(auth, "Bearer ") {
		return auth[7:] == cfg.APIKey
	}
	return false
}

// ------------------------------- Handler -------------------------------

var upstreamClient = &http.Client{
	Transport: &http.Transport{
		DialContext:         (&net.Dialer{Timeout: 10 * time.Second}).DialContext,
		TLSHandshakeTimeout: 10 * time.Second,
	},
}

func handleModels(w http.ResponseWriter, r *http.Request) {
	cfg := store.Get()
	if !checkAuth(r, cfg) {
		sendJSON(w, 401, errObj("invalid api key", "auth_error"))
		return
	}
	models := activeModels(cfg)
	data := make([]map[string]string, 0, len(models))
	for _, k := range modelNames(models) {
		data = append(data, map[string]string{"id": k, "object": "model", "owned_by": "aigate"})
	}
	sendJSON(w, 200, map[string]interface{}{"object": "list", "data": data})
}

func handleForward(w http.ResponseWriter, r *http.Request) {
	cfg := store.Get()
	if !checkAuth(r, cfg) {
		sendJSON(w, 401, errObj("invalid api key", "auth_error"))
		return
	}
	body, err := io.ReadAll(r.Body)
	if err != nil {
		sendJSON(w, 400, errObj("read body failed", "bad_request"))
		return
	}
	var payload map[string]interface{}
	if err := json.Unmarshal(body, &payload); err != nil {
		sendJSON(w, 400, errObj("invalid json body", "bad_request"))
		return
	}

	reqModelVal, hasModel := payload["model"]
	if !hasModel {
		sendJSON(w, 404, errObj(fmt.Sprintf("unsupported path: %s", r.URL.Path), "not_found"))
		return
	}
	reqModel, _ := reqModelVal.(string)
	models := activeModels(cfg)
	up, ok := models[reqModel]
	if !ok {
		sendJSON(w, 404, errObj(
			fmt.Sprintf("model '%s' not in active route '%s'; available: %v", reqModel, cfg.ActiveRoute, modelNames(models)),
			"model_not_found"))
		return
	}

	upModel := up.UpstreamModel
	if upModel == "" {
		upModel = reqModel
	}
	payload["model"] = upModel
	newBody, _ := json.Marshal(payload)

	url := joinURL(up.BaseURL, r.URL.Path)
	req, err := http.NewRequest(r.Method, url, bytes.NewReader(newBody))
	if err != nil {
		sendJSON(w, 502, errObj("build request failed: "+err.Error(), "upstream_error"))
		return
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+up.APIKey)

	isStream, _ := payload["stream"].(bool)

	resp, err := upstreamClient.Do(req)
	if err != nil {
		sendJSON(w, 502, errObj("upstream connect failed: "+err.Error(), "upstream_error"))
		return
	}
	defer resp.Body.Close()

	if resp.StatusCode >= 400 {
		errBody, _ := io.ReadAll(resp.Body)
		sendJSON(w, resp.StatusCode, errObj("upstream error: "+string(errBody), "upstream_error"))
		return
	}

	if isStream {
		w.Header().Set("Content-Type", "text/event-stream; charset=utf-8")
		w.Header().Set("Cache-Control", "no-cache")
		w.WriteHeader(200)
		flusher, _ := w.(http.Flusher)
		buf := make([]byte, 4096)
		for {
			n, rerr := resp.Body.Read(buf)
			if n > 0 {
				w.Write(buf[:n])
				if flusher != nil {
					flusher.Flush()
				}
			}
			if rerr != nil {
				break
			}
		}
	} else {
		ct := resp.Header.Get("Content-Type")
		if ct == "" {
			ct = "application/json"
		}
		w.Header().Set("Content-Type", ct)
		w.WriteHeader(200)
		io.Copy(w, resp.Body)
	}
}

func mainHandler(w http.ResponseWriter, r *http.Request) {
	path := r.URL.Path
	log.Printf("[aigate] %s %s %s", r.RemoteAddr, r.Method, path)
	if r.Method == http.MethodGet {
		switch path {
		case "/", "/health":
			sendJSON(w, 200, map[string]string{"status": "ok", "service": "aigate"})
			return
		case "/v1/models", "/models":
			handleModels(w, r)
			return
		}
		sendJSON(w, 404, errObj(fmt.Sprintf("not found: %s", path), "not_found"))
		return
	}
	if r.Method == http.MethodPost {
		handleForward(w, r)
		return
	}
	sendJSON(w, 404, errObj(fmt.Sprintf("not found: %s", path), "not_found"))
}

// ------------------------------- CLI -------------------------------

func genKey() string {
	b := make([]byte, 16)
	rand.Read(b)
	return "aigate-sk-" + hex.EncodeToString(b)
}

func cmdInit() {
	fs := flag.NewFlagSet("init", flag.ExitOnError)
	force := fs.Bool("force", false, "overwrite existing config")
	fs.Parse(os.Args[2:])

	if _, err := os.Stat(configPath); err == nil && !*force {
		fmt.Printf("config already exists: %s  (use --force to overwrite)\n", configPath)
		return
	}
	cfg := defaultConfig()
	cfg.APIKey = genKey()
	if err := saveConfig(cfg); err != nil {
		fmt.Println("error:", err)
		return
	}
	fmt.Printf("created config: %s\n", configPath)
	fmt.Printf("api_key:        %s\n", cfg.APIKey)
	fmt.Printf("server:         http://%s:%d/v1\n", cfg.Server.Host, cfg.Server.Port)
	fmt.Println("next: `aigate routes add ...` 添加模型，`aigate serve` 启动")
}

func cmdServe() {
	fs := flag.NewFlagSet("serve", flag.ExitOnError)
	host := fs.String("host", "", "bind host")
	port := fs.Int("port", 0, "bind port")
	fs.Parse(os.Args[2:])

	cfg := store.Get()
	h, p := cfg.Server.Host, cfg.Server.Port
	if *host != "" {
		h = *host
	}
	if *port != 0 {
		p = *port
	}
	names := modelNames(activeModels(cfg))
	fmt.Printf("aigate listening on http://%s:%d/v1\n", h, p)
	fmt.Printf("active route: %s  (%d models: %v)\n", cfg.ActiveRoute, len(names), names)
	fmt.Println("Ctrl+C to stop")

	mux := http.NewServeMux()
	mux.HandleFunc("/", mainHandler)
	srv := &http.Server{Addr: fmt.Sprintf("%s:%d", h, p), Handler: mux}
	log.Fatal(srv.ListenAndServe())
}

func cmdStatus() {
	cfg := store.Get()
	fmt.Printf("config:       %s\n", configPath)
	fmt.Printf("server:       http://%s:%d/v1\n", cfg.Server.Host, cfg.Server.Port)
	fmt.Printf("api_key:      %s\n", cfg.APIKey)
	fmt.Printf("auth:         %v\n", cfg.Auth)
	fmt.Printf("active_route: %s\n", cfg.ActiveRoute)
	models := activeModels(cfg)
	fmt.Printf("models (%d):\n", len(models))
	for _, m := range modelNames(models) {
		up := models[m]
		um := up.UpstreamModel
		if um == "" {
			um = m
		}
		fmt.Printf("  - %s -> %s  (upstream: %s)\n", m, up.BaseURL, um)
	}
}

func cmdKey() {
	fs := flag.NewFlagSet("key", flag.ExitOnError)
	rotate := fs.Bool("rotate", false, "regenerate key")
	fs.Parse(os.Args[2:])

	cfg := store.Get()
	if *rotate {
		cfg.APIKey = genKey()
		saveConfig(cfg)
		store.Reload()
	}
	fmt.Printf("api_key: %s\n", cfg.APIKey)
	fmt.Printf("server:  http://%s:%d/v1\n", cfg.Server.Host, cfg.Server.Port)
}

func cmdSwitch() {
	fs := flag.NewFlagSet("switch", flag.ExitOnError)
	fs.Parse(os.Args[2:])
	if fs.NArg() < 1 {
		fmt.Println("usage: aigate switch <route>")
		return
	}
	name := fs.Arg(0)
	cfg := store.Get()
	if _, ok := cfg.Routes[name]; !ok {
		fmt.Printf("route not found: %s\n", name)
		avail := make([]string, 0, len(cfg.Routes))
		for k := range cfg.Routes {
			avail = append(avail, k)
		}
		sort.Strings(avail)
		fmt.Println("available:", avail)
		return
	}
	cfg.ActiveRoute = name
	saveConfig(cfg)
	store.Reload()
	names := modelNames(activeModels(cfg))
	fmt.Printf("switched active route -> %s  (%d models: %v)\n", name, len(names), names)
}

func cmdRoutes() {
	if len(os.Args) < 3 {
		fmt.Println("usage: aigate routes <list|add|remove|show>")
		return
	}
	action := os.Args[2]
	cfg := store.Get()

	switch action {
	case "list":
		if len(cfg.Routes) == 0 {
			fmt.Println("(no routes)")
			return
		}
		names := make([]string, 0, len(cfg.Routes))
		for k := range cfg.Routes {
			names = append(names, k)
		}
		sort.Strings(names)
		for _, name := range names {
			mark := " "
			if name == cfg.ActiveRoute {
				mark = "*"
			}
			models := cfg.Routes[name].Models
			fmt.Printf("%s %s  (%d models)\n", mark, name, len(models))
			for _, m := range modelNames(models) {
				up := models[m]
				um := up.UpstreamModel
				if um == "" {
					um = m
				}
				fmt.Printf("      - %s -> %s  (upstream: %s)\n", m, up.BaseURL, um)
			}
		}

	case "add":
		fs := flag.NewFlagSet("routes add", flag.ExitOnError)
		route := fs.String("route", "", "route name")
		model := fs.String("model", "", "model alias")
		baseURL := fs.String("base-url", "", "upstream base url")
		apiKey := fs.String("api-key", "", "upstream api key")
		upModel := fs.String("upstream-model", "", "upstream real model name")
		fs.Parse(os.Args[3:])
		if *route == "" || *model == "" || *baseURL == "" || *apiKey == "" {
			fmt.Println("usage: aigate routes add --route R --model M --base-url U --api-key K [--upstream-model M']")
			return
		}
		um := *upModel
		if um == "" {
			um = *model
		}
		if cfg.Routes == nil {
			cfg.Routes = map[string]Route{}
		}
		r, ok := cfg.Routes[*route]
		if !ok {
			r = Route{Models: map[string]ModelUpstream{}}
		}
		if r.Models == nil {
			r.Models = map[string]ModelUpstream{}
		}
		r.Models[*model] = ModelUpstream{BaseURL: *baseURL, APIKey: *apiKey, UpstreamModel: um}
		cfg.Routes[*route] = r
		if cfg.ActiveRoute == "" {
			cfg.ActiveRoute = *route
		}
		saveConfig(cfg)
		store.Reload()
		fmt.Printf("added model '%s' to route '%s'\n", *model, *route)
		if cfg.ActiveRoute == *route {
			fmt.Printf("(route '%s' is now active)\n", *route)
		}

	case "remove":
		fs := flag.NewFlagSet("routes remove", flag.ExitOnError)
		route := fs.String("route", "", "route name")
		model := fs.String("model", "", "model to remove (empty = remove whole route)")
		fs.Parse(os.Args[3:])
		if *route == "" {
			fmt.Println("usage: aigate routes remove --route R [--model M]")
			return
		}
		r, ok := cfg.Routes[*route]
		if !ok {
			fmt.Printf("route not found: %s\n", *route)
			return
		}
		if *model != "" {
			if _, ok := r.Models[*model]; ok {
				delete(r.Models, *model)
				cfg.Routes[*route] = r
				fmt.Printf("removed model '%s' from route '%s'\n", *model, *route)
			} else {
				fmt.Printf("model not in route: %s\n", *model)
			}
		} else {
			delete(cfg.Routes, *route)
			if cfg.ActiveRoute == *route {
				cfg.ActiveRoute = ""
			}
			fmt.Printf("removed route '%s'\n", *route)
		}
		saveConfig(cfg)
		store.Reload()

	case "show":
		fs := flag.NewFlagSet("routes show", flag.ExitOnError)
		route := fs.String("route", "", "route name")
		fs.Parse(os.Args[3:])
		r, ok := cfg.Routes[*route]
		if !ok {
			fmt.Printf("route not found: %s\n", *route)
			return
		}
		data, _ := json.MarshalIndent(r, "", "  ")
		fmt.Println(string(data))

	default:
		fmt.Println("usage: aigate routes <list|add|remove|show>")
	}
}

func printUsage() {
	fmt.Println(`aigate — 本地 LLM 模型路由聚合网关

usage: aigate <command> [options]

commands:
  init [--force]              初始化配置
  serve [--host H] [--port P] 启动网关
  status                      查看状态
  key [--rotate]              查看/轮换 api_key
  switch <route>              切换激活路由组
  routes list                 列出路由组
  routes add --route R --model M --base-url U --api-key K [--upstream-model M']
  routes remove --route R [--model M]
  routes show --route R`)
}

func main() {
	configDir = resolveConfigDir()
	configPath = filepath.Join(configDir, "config.json")

	if len(os.Args) < 2 {
		printUsage()
		return
	}
	switch os.Args[1] {
	case "init":
		cmdInit()
	case "serve":
		cmdServe()
	case "status":
		cmdStatus()
	case "key":
		cmdKey()
	case "switch":
		cmdSwitch()
	case "routes":
		cmdRoutes()
	case "-h", "--help":
		printUsage()
	default:
		fmt.Printf("unknown command: %s\n\n", os.Args[1])
		printUsage()
	}
}
