package main

import (
	"bytes"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"os"
	"os/signal"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/gorilla/websocket"
)

// ======================== Configuration ========================

type LLMConfig struct {
	Provider     string  `json:"provider"`
	APIURL       string  `json:"api_url"`
	APIKey       string  `json:"api_key"`
	Model        string  `json:"model"`
	SystemPrompt string  `json:"system_prompt"`
	MaxTokens    int     `json:"max_tokens"`
	Temperature  float64 `json:"temperature"`
}

type TTSConfig struct {
	Provider string `json:"provider"`
	Voice    string `json:"voice"`
	Rate     int    `json:"rate"`
}

type OpenClawConfig struct {
	Enabled    bool   `json:"enabled"`
	ServerURL  string `json:"server_url"`
	PluginPoll bool   `json:"plugin_poll"`
}

type BindConfig struct {
	Enabled      bool   `json:"enabled"`
	BindCommand  string `json:"bind_command"`
}

type AppConfig struct {
	Port     int            `json:"port"`
	LLM      LLMConfig      `json:"llm"`
	TTS      TTSConfig      `json:"tts"`
	OpenClaw OpenClawConfig `json:"openclaw"`
	Bind     BindConfig     `json:"bind"`
}

func defaultConfig() AppConfig {
	return AppConfig{
		Port: 8003,
		LLM: LLMConfig{
			Provider:     "openai",
			APIURL:       "https://api.openai.com/v1/chat/completions",
			APIKey:       "",
			Model:        "gpt-4o-mini",
			SystemPrompt: "你是一个名叫小智的AI语音助手。请用简洁自然的中文回复，每次回复控制在2-3句话以内。",
			MaxTokens:    500,
			Temperature:  0.7,
		},
		TTS: TTSConfig{
			Provider: "edge",
			Voice:    "zh-CN-XiaoxiaoNeural",
			Rate:     0,
		},
		OpenClaw: OpenClawConfig{
			Enabled:    false,
			ServerURL:  "http://127.0.0.1:18789",
			PluginPoll: false,
		},
		Bind: BindConfig{
			Enabled:     true,
			BindCommand: "!bind",
		},
	}
}

func loadConfig(path string) (*AppConfig, error) {
	cfg := defaultConfig()
	data, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			// Write default config
			b, _ := json.MarshalIndent(cfg, "", "  ")
			os.WriteFile(path, b, 0644)
			log.Printf("[config] Created default config: %s", path)
			return &cfg, nil
		}
		return nil, err
	}
	if err := json.Unmarshal(data, &cfg); err != nil {
		return nil, fmt.Errorf("parse config: %w", err)
	}
	// Apply env-var overrides
	if port := os.Getenv("XIAOZHI_SERVER_PORT"); port != "" {
		cfg.Port, _ = strconv.Atoi(port)
	}
	if key := os.Getenv("XIAOZHI_LLM_KEY"); key != "" {
		cfg.LLM.APIKey = key
	}
	if url := os.Getenv("XIAOZHI_LLM_URL"); url != "" {
		cfg.LLM.APIURL = url
	}
	return &cfg, nil
}

// ======================== Message types ========================

type Message struct {
	ID        int    `json:"id"`
	DeviceID  string `json:"device_id"`
	Text      string `json:"text"`
	Timestamp int64  `json:"timestamp"`
	Source    string `json:"source"`
}

type Binding struct {
	Token     string `json:"token"`
	ServerURL string `json:"server_url"`
	CreatedAt int64  `json:"created_at"`
	Confirmed bool   `json:"confirmed"`
}

type PendingReq struct {
	DeviceID string
	Resolve  chan struct{}
}

// ======================== Server ========================

type Server struct {
	cfg *AppConfig

	mu          sync.Mutex
	queues      map[string][]Message
	nextMsgID   int
	pending     map[int]*PendingReq
	pendingID   int
	bindings    map[string]*Binding
	deviceConns map[string]map[*websocket.Conn]bool
	// Per-device LLM contexts
	chatHistories map[string][]map[string]string
}

var upgrader = websocket.Upgrader{
	CheckOrigin: func(r *http.Request) bool { return true },
}

func NewServer(cfg *AppConfig) *Server {
	s := &Server{
		cfg:           cfg,
		queues:        make(map[string][]Message),
		pending:       make(map[int]*PendingReq),
		bindings:      make(map[string]*Binding),
		deviceConns:   make(map[string]map[*websocket.Conn]bool),
		chatHistories: make(map[string][]map[string]string),
	}
	go s.cleanupLoop()
	return s
}

func (s *Server) cleanupLoop() {
	t := time.NewTicker(60 * time.Second)
	for range t.C {
		s.mu.Lock()
		cutoff := time.Now().UnixMilli() - 10*60*1000
		for id, msgs := range s.queues {
			filtered := make([]Message, 0)
			for _, m := range msgs {
				if m.Timestamp > cutoff {
					filtered = append(filtered, m)
				}
			}
			if len(filtered) == 0 {
				delete(s.queues, id)
			} else {
				s.queues[id] = filtered
			}
		}
		// Also clean old chat histories
		for id, hist := range s.chatHistories {
			if len(hist) == 0 {
				delete(s.chatHistories, id)
				continue
			}
			// Keep last 20 messages
			if len(hist) > 20 {
				s.chatHistories[id] = hist[len(hist)-20:]
			}
		}
		s.mu.Unlock()
	}
}

// ======================== Message queue ========================

func (s *Server) addMessage(deviceID, text, source string) Message {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.nextMsgID++
	m := Message{
		ID:        s.nextMsgID,
		DeviceID:  deviceID,
		Text:      strings.TrimSpace(text),
		Timestamp: time.Now().UnixMilli(),
		Source:    source,
	}
	s.queues[deviceID] = append(s.queues[deviceID], m)
	return m
}

func (s *Server) notifyPending(deviceID string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	for id, p := range s.pending {
		if p.DeviceID == deviceID || p.DeviceID == "*" {
			close(p.Resolve)
			delete(s.pending, id)
		}
	}
}

func (s *Server) waitForMessages(deviceID string, offset int, timeoutSec int) []Message {
	s.mu.Lock()
	msgs := s.queues[deviceID]
	var updates []Message
	for _, m := range msgs {
		if m.ID > offset {
			updates = append(updates, m)
		}
	}
	if len(updates) > 0 {
		s.mu.Unlock()
		return updates
	}

	pendingID := s.pendingID
	s.pendingID++
	ch := make(chan struct{})
	s.pending[pendingID] = &PendingReq{DeviceID: deviceID, Resolve: ch}
	s.mu.Unlock()

	select {
	case <-ch:
	case <-time.After(time.Duration(timeoutSec) * time.Second):
	}

	s.mu.Lock()
	delete(s.pending, pendingID)
	msgs = s.queues[deviceID]
	updates = nil
	for _, m := range msgs {
		if m.ID > offset {
			updates = append(updates, m)
		}
	}
	s.mu.Unlock()
	return updates
}

// ======================== LLM calling ========================

func (s *Server) callLLM(deviceID, userMsg string) (string, error) {
	cfg := &s.cfg.LLM
	if cfg.APIKey == "" {
		return "", fmt.Errorf("LLM API key not configured")
	}

	s.mu.Lock()
	history := s.chatHistories[deviceID]
	if history == nil {
		history = []map[string]string{}
	}
	// Append user message
	history = append(history, map[string]string{"role": "user", "content": userMsg})
	s.chatHistories[deviceID] = history
	// Build messages array
	messages := []map[string]string{
		{"role": "system", "content": cfg.SystemPrompt},
	}
	messages = append(messages, history...)
	s.mu.Unlock()

	body := map[string]interface{}{
		"model":       cfg.Model,
		"messages":    messages,
		"max_tokens":  cfg.MaxTokens,
		"temperature": cfg.Temperature,
	}
	jsonBody, _ := json.Marshal(body)

	req, err := http.NewRequest("POST", cfg.APIURL, bytes.NewReader(jsonBody))
	if err != nil {
		return "", err
	}
	req.Header.Set("Content-Type", "application/json")
	if cfg.APIKey != "" {
		req.Header.Set("Authorization", "Bearer "+cfg.APIKey)
	}

	client := &http.Client{Timeout: 30 * time.Second}
	resp, err := client.Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()

	respBody, _ := io.ReadAll(resp.Body)
	if resp.StatusCode != 200 {
		return "", fmt.Errorf("LLM returned %d: %.200s", resp.StatusCode, respBody)
	}

	var result struct {
		Choices []struct {
			Message struct {
				Content string `json:"content"`
			} `json:"message"`
		} `json:"choices"`
	}
	if err := json.Unmarshal(respBody, &result); err != nil {
		return "", err
	}
	if len(result.Choices) == 0 {
		return "", fmt.Errorf("no response from LLM")
	}

	reply := strings.TrimSpace(result.Choices[0].Message.Content)
	if reply == "" {
		return "", fmt.Errorf("empty LLM response")
	}

	// Save assistant reply to history
	s.mu.Lock()
	s.chatHistories[deviceID] = append(s.chatHistories[deviceID],
		map[string]string{"role": "assistant", "content": reply})
	s.mu.Unlock()

	return reply, nil
}

// ======================== Edge TTS ========================

func (s *Server) synthesizeTTS(text string) ([]byte, error) {
	// Use Microsoft Edge TTS (free, no API key needed)
	voice := s.cfg.TTS.Voice
	rate := s.cfg.TTS.Rate
	if rate == 0 {
		rate = 0
	}

	// SSML payload
	ssml := fmt.Sprintf(
		`<speak version="1.0" xmlns="http://www.w3.org/2001/10/synthesis" xml:lang="zh-CN">
			<voice name="%s">
				<prosody rate="%+d%%">%s</prosody>
			</voice>
		</speak>`,
		voice, rate, text)

	url := fmt.Sprintf(
		"https://speech.platform.bing.com/consumer/speech/synthesize/readaloud/edge/v1?TrustedClientToken=6A5AA1D4EAFF4E9FB37E23D68491D6F4",
	)

	req, err := http.NewRequest("POST", url, strings.NewReader(ssml))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/ssml+xml")
	req.Header.Set("X-Microsoft-OutputFormat", "audio-16khz-64kbitrate-mono-mp3")
	req.Header.Set("User-Agent", "Mozilla/5.0")

	client := &http.Client{Timeout: 15 * time.Second}
	resp, err := client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	if resp.StatusCode != 200 {
		return nil, fmt.Errorf("TTS returned %d", resp.StatusCode)
	}
	return io.ReadAll(resp.Body)
}

// ======================== Binding ========================

func (s *Server) requestBind(deviceID, host string) (string, string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	tokenBytes := make([]byte, 16)
	rand.Read(tokenBytes)
	token := hex.EncodeToString(tokenBytes)
	bindURL := fmt.Sprintf("http://%s/xiaozhi/bind/confirm?device=%s&token=%s", host, deviceID, token)
	s.bindings[deviceID] = &Binding{
		Token:     token,
		ServerURL: fmt.Sprintf("http://%s", host),
		CreatedAt: time.Now().UnixMilli(),
		Confirmed: false,
	}
	log.Printf("[bind] device=%s url=%s", deviceID, bindURL)
	return bindURL, token
}

func (s *Server) confirmBind(deviceID, token string) (bool, string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	b, ok := s.bindings[deviceID]
	if !ok {
		return false, "no binding request for this device"
	}
	if b.Token != token {
		return false, "invalid token"
	}
	b.Confirmed = true
	log.Printf("[bind] confirmed: device=%s", deviceID)
	return true, "设备绑定成功！"
}

// ======================== Broadcast ========================

func (s *Server) broadcastToDevice(deviceID string, data interface{}) {
	s.mu.Lock()
	conns := s.deviceConns[deviceID]
	s.mu.Unlock()
	if conns == nil {
		return
	}
	payload, _ := json.Marshal(data)
	for ws := range conns {
		ws.WriteMessage(websocket.TextMessage, payload)
	}
}

// ======================== HTTP handlers ========================

func (s *Server) handleHealth(w http.ResponseWriter, r *http.Request) {
	json.NewEncoder(w).Encode(map[string]interface{}{
		"status":        "ok",
		"version":       "2.1.0",
		"uptime":        time.Now().UnixMilli(),
		"llm_configured": s.cfg.LLM.APIKey != "",
		"openclaw_enabled": s.cfg.OpenClaw.Enabled,
	})
}

func (s *Server) handleOta(w http.ResponseWriter, r *http.Request) {
	// If POST, consume the body (ESP32 sends system info)
	if r.Method == "POST" {
		body, _ := io.ReadAll(r.Body)
		if len(body) > 0 {
			var info map[string]interface{}
			if json.Unmarshal(body, &info) == nil {
				log.Printf("[ota] device info: mac=%v", info["mac"])
			}
		}
	}

	port := s.cfg.Port
	wsURL := fmt.Sprintf("ws://%s:%d/xiaozhi/v1", r.Host, port)
	// If Host already has port, don't add again
	if strings.Contains(r.Host, ":") {
		parts := strings.SplitN(r.Host, ":", 2)
		wsURL = fmt.Sprintf("ws://%s:%s/xiaozhi/v1", parts[0], parts[1])
	}
	resp := map[string]interface{}{
		"firmware": map[string]string{
			"version": "0.0.0",
			"url":     "",
		},
		"websocket": map[string]interface{}{
			"url":     wsURL,
			"version": 3,
		},
		"server_time": map[string]interface{}{
			"timestamp":       time.Now().UnixMilli(),
					"timezone_offset": -int(time.Now().Local().UnixNano())/60000000000,
		},
	}
	if s.cfg.LLM.APIKey != "" {
		resp["llm"] = map[string]string{
			"model": s.cfg.LLM.Model,
		}
	}
	log.Printf("[ota] ws=%s", wsURL)
	json.NewEncoder(w).Encode(resp)
}

func (s *Server) handleUpdates(w http.ResponseWriter, r *http.Request) {
	deviceID := r.URL.Query().Get("device_id")
	offset, _ := strconv.Atoi(r.URL.Query().Get("offset"))
	timeout, _ := strconv.Atoi(r.URL.Query().Get("timeout"))
	if timeout <= 0 || timeout > 60 {
		timeout = 30
	}

	updates := s.waitForMessages(deviceID, offset, timeout)
	if updates == nil {
		updates = []Message{}
	}
	json.NewEncoder(w).Encode(map[string]interface{}{
		"ok":     true,
		"result": updates,
	})
}

func (s *Server) handleReply(w http.ResponseWriter, r *http.Request) {
	var body struct {
		DeviceID string `json:"device_id"`
		Text     string `json:"text"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil || body.DeviceID == "" || body.Text == "" {
		w.WriteHeader(400)
		json.NewEncoder(w).Encode(map[string]interface{}{"ok": false, "error": "device_id and text required"})
		return
	}

	// Check for bind command
	if s.cfg.Bind.Enabled && (body.Text == s.cfg.Bind.BindCommand || strings.HasPrefix(body.Text, s.cfg.Bind.BindCommand+" ")) {
		bindURL, _ := s.requestBind(body.DeviceID, r.Host)
		s.broadcastToDevice(body.DeviceID, map[string]interface{}{
			"type": "bind_qr",
			"url":  bindURL,
		})
		s.broadcastToDevice(body.DeviceID, map[string]string{
			"type":      "reply",
			"device_id": body.DeviceID,
			"text":      "请在设备屏幕查看微信绑定二维码",
		})
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(map[string]interface{}{"ok": true, "bind": true, "bind_url": bindURL})
		return
	}

	// Forward the reply as TTS text to the device
	s.broadcastToDevice(body.DeviceID, map[string]interface{}{
		"type":      "tts",
		"state":     "sentence_start",
		"device_id": body.DeviceID,
		"text":      body.Text,
	})
	// Signal TTS start
	s.broadcastToDevice(body.DeviceID, map[string]string{
		"type": "tts", "state": "start",
	})

	// If LLM is configured, generate TTS audio
	if s.cfg.LLM.APIKey != "" && s.cfg.TTS.Provider == "edge" {
		go func() {
			audio, err := s.synthesizeTTS(body.Text)
			if err != nil {
				log.Printf("[tts] synthesis error: %v", err)
				s.broadcastToDevice(body.DeviceID, map[string]string{
					"type": "tts", "state": "stop",
				})
				return
			}
			// Send audio as binary WebSocket message
			s.mu.Lock()
			conns := s.deviceConns[body.DeviceID]
			s.mu.Unlock()
			if conns != nil {
				for ws := range conns {
					ws.WriteMessage(websocket.BinaryMessage, audio)
				}
			}
			s.broadcastToDevice(body.DeviceID, map[string]string{
				"type": "tts", "state": "stop",
			})
		}()
	} else {
		// If no TTS, send stop immediately
		s.broadcastToDevice(body.DeviceID, map[string]string{
			"type": "tts", "state": "stop",
		})
	}

	log.Printf("[reply] to %s: %.100s", body.DeviceID, body.Text)
	json.NewEncoder(w).Encode(map[string]interface{}{"ok": true})
}

func (s *Server) handleBindRequest(w http.ResponseWriter, r *http.Request) {
	var body struct {
		DeviceID string `json:"device_id"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil || body.DeviceID == "" {
		w.WriteHeader(400)
		json.NewEncoder(w).Encode(map[string]interface{}{"ok": false, "error": "device_id required"})
		return
	}
	bindURL, _ := s.requestBind(body.DeviceID, r.Host)
	s.broadcastToDevice(body.DeviceID, map[string]string{
		"type": "bind_qr",
		"url":  bindURL,
	})
	log.Printf("[bind] QR sent to device=%s", body.DeviceID)
	json.NewEncoder(w).Encode(map[string]interface{}{
		"ok":        true,
		"bind_url":  bindURL,
		"device_id": body.DeviceID,
	})
}

func (s *Server) handleBindConfirm(w http.ResponseWriter, r *http.Request) {
	deviceID := r.URL.Query().Get("device")
	token := r.URL.Query().Get("token")
	ok, msg := s.confirmBind(deviceID, token)
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	if ok {
		fmt.Fprintf(w, `<!DOCTYPE html><html><head><meta charset="utf-8"><meta name="viewport" content="width=device-width,initial-scale=1"><title>喵小智 - 绑定成功</title><style>body{font-family:-apple-system,sans-serif;display:flex;align-items:center;justify-content:center;height:100vh;margin:0;background:#f0fdf4}.card{text-align:center;padding:40px;border-radius:20px;background:#fff;box-shadow:0 4px 20px rgba(0,0,0,.1)}h1{color:#16a34a;font-size:48px;margin:0}p{color:#4b5563;margin:16px 0 0}</style></head><body><div class="card"><h1>✅</h1><p>喵小智绑定成功！<br>设备 %s</p></div></body></html>`, deviceID)
	} else {
		fmt.Fprintf(w, `<!DOCTYPE html><html><head><meta charset="utf-8"><meta name="viewport" content="width=device-width,initial-scale=1"><title>喵小智 - 绑定失败</title><style>body{font-family:-apple-system,sans-serif;display:flex;align-items:center;justify-content:center;height:100vh;margin:0;background:#fef2f2}.card{text-align:center;padding:40px;border-radius:20px;background:#fff;box-shadow:0 4px 20px rgba(0,0,0,.1)}h1{color:#dc2626;font-size:48px;margin:0}p{color:#4b5563;margin:16px 0 0}</style></head><body><div class="card"><h1>❌</h1><p>绑定失败：%s</p></div></body></html>`, msg)
	}
}

func (s *Server) handleBindStatus(w http.ResponseWriter, r *http.Request) {
	deviceID := r.URL.Query().Get("device_id")
	s.mu.Lock()
	b, ok := s.bindings[deviceID]
	s.mu.Unlock()
	bound := false
	if ok && b.Confirmed {
		bound = true
	}
	json.NewEncoder(w).Encode(map[string]interface{}{
		"device_id": deviceID,
		"bound":     bound,
	})
}

// ======================== WebSocket handler ========================

func (s *Server) handleWebSocket(w http.ResponseWriter, r *http.Request) {
	deviceID := r.URL.Query().Get("device_id")
	if deviceID == "" {
		deviceID = "unknown"
	}

	ws, err := upgrader.Upgrade(w, r, nil)
	if err != nil {
		log.Printf("[ws] upgrade error: %v", err)
		return
	}

	clientID := make([]byte, 4)
	rand.Read(clientID)
	cid := hex.EncodeToString(clientID)
	sessionID := cid
	log.Printf("[ws] device connected: %s (%s)", deviceID, cid)

	s.mu.Lock()
	if s.deviceConns[deviceID] == nil {
		s.deviceConns[deviceID] = make(map[*websocket.Conn]bool)
	}
	s.deviceConns[deviceID][ws] = true
	s.mu.Unlock()

	helloDone := false

	// Read loop
	for {
		_, raw, err := ws.ReadMessage()
		if err != nil {
			break
		}
		text := strings.TrimSpace(string(raw))
		if text == "" {
			continue
		}

		var msg map[string]interface{}
		if err := json.Unmarshal(raw, &msg); err != nil {
			// Binary audio data or non-JSON
			log.Printf("[ws] device %s: raw data (%d bytes)", deviceID, len(raw))
			continue
		}

		msgType, _ := msg["type"].(string)

		// ---- Xiaozhi hello handshake ----
		if msgType == "hello" && !helloDone {
			helloDone = true
			espAudioParams, _ := msg["audio_params"].(map[string]interface{})
			espVersion, _ := msg["version"].(float64)
			if espVersion == 0 { espVersion = 1 }
			espFeatures, _ := msg["features"].(map[string]interface{})

			hasMCP := false
			if mcp, ok := espFeatures["mcp"].(bool); ok {
				hasMCP = mcp
			}

			sampleRate := 16000
			if sr, ok := espAudioParams["sample_rate"].(float64); ok {
				sampleRate = int(sr)
			}
			format := "opus"
			if f, ok := espAudioParams["format"].(string); ok {
				format = f
			}
			channels := 1
			if ch, ok := espAudioParams["channels"].(float64); ok {
				channels = int(ch)
			}
			frameDuration := 60
			if fd, ok := espAudioParams["frame_duration"].(float64); ok {
				frameDuration = int(fd)
			}

			// Server hello response
			ws.WriteJSON(map[string]interface{}{
				"type":       "hello",
				"transport":  "websocket",
				"session_id": sessionID,
				"version":    espVersion,
				"audio_params": map[string]interface{}{
					"format":         format,
					"sample_rate":    sampleRate,
					"channels":       channels,
					"frame_duration": frameDuration,
				},
				"features": map[string]interface{}{
					"mcp": hasMCP,
				},
			})
			log.Printf("[ws] hello handshake OK: device=%s mcp=%v rate=%d session=%s",
				deviceID, hasMCP, sampleRate, sessionID)
			continue
		}

		// ---- STT: speech recognition result ----
		if msgType == "stt" {
			if t, ok := msg["text"].(string); ok && strings.TrimSpace(t) != "" {
				log.Printf("[stt] %s: %.100s", deviceID, t)
				s.addMessage(deviceID, t, "local")

				// If LLM is configured, process directly
				if s.cfg.LLM.APIKey != "" {
					go func(did, userText string) {
						log.Printf("[llm] processing user message from %s", did)
						reply, err := s.callLLM(did, userText)
						if err != nil {
							log.Printf("[llm] error: %v", err)
							// Fallback: let plugin/OpenClaw handle it
							s.notifyPending(did)
							return
						}
						log.Printf("[llm] reply to %s: %.100s", did, reply)
						// Add reply to queue for plugin polling
						s.addMessage(did, reply, "llm")
						// Send TTS to ESP32
						s.broadcastToDevice(did, map[string]interface{}{
							"type":       "tts",
							"state":      "start",
						})
						s.broadcastToDevice(did, map[string]interface{}{
							"type":       "tts",
							"state":      "sentence_start",
							"text":       reply,
						})
						// TTS synthesis
						if s.cfg.TTS.Provider == "edge" {
							audio, err := s.synthesizeTTS(reply)
							if err != nil {
								log.Printf("[tts] synthesis error: %v", err)
							} else {
								s.mu.Lock()
								conns := s.deviceConns[deviceID]
								s.mu.Unlock()
								if conns != nil {
									for w := range conns {
										w.WriteMessage(websocket.BinaryMessage, audio)
									}
								}
							}
						}
						s.broadcastToDevice(did, map[string]string{
							"type": "tts", "state": "stop",
						})
						s.notifyPending(did)
					}(deviceID, t)
				} else {
					// No LLM configured — notify plugin for polling
					s.notifyPending(deviceID)
				}
			}
			continue
		}

		// ---- Other protocol messages ----
		switch msgType {
		case "ping":
			ws.WriteJSON(map[string]string{"type": "pong"})
			continue
		case "tts", "listen", "mcp", "llm":
			continue
		case "text":
			if t, ok := msg["text"].(string); ok && strings.TrimSpace(t) != "" {
				log.Printf("[text] device %s: %.100s", deviceID, t)
				s.addMessage(deviceID, t, "local")
				s.notifyPending(deviceID)
			}
			continue
		}

		// Fallback: plain text
		log.Printf("[ws] device %s: %.100s", deviceID, text)
		s.addMessage(deviceID, text, "local")
		s.notifyPending(deviceID)
	}

	// Cleanup on disconnect
	s.mu.Lock()
	delete(s.deviceConns[deviceID], ws)
	if len(s.deviceConns[deviceID]) == 0 {
		delete(s.deviceConns, deviceID)
	}
	s.mu.Unlock()
	log.Printf("[ws] device disconnected: %s (%s)", deviceID, cid)
}

// ======================== Main ========================

func main() {
	log.SetFlags(log.Ldate | log.Ltime | log.Lmicroseconds)

	// Find config file
	configPath := "config.json"
	if len(os.Args) > 1 {
		configPath = os.Args[1]
	}
	if env := os.Getenv("XIAOZHI_CONFIG"); env != "" {
		configPath = env
	}

	cfg, err := loadConfig(configPath)
	if err != nil {
		log.Fatalf("Failed to load config: %v", err)
	}

	srv := NewServer(cfg)

	mux := http.NewServeMux()
	handler := func(h http.HandlerFunc) http.HandlerFunc {
		return func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("Access-Control-Allow-Origin", "*")
			w.Header().Set("Access-Control-Allow-Methods", "GET, POST, OPTIONS")
			w.Header().Set("Access-Control-Allow-Headers", "Content-Type")
			if r.Method == "OPTIONS" {
				w.WriteHeader(204)
				return
			}
			h(w, r)
		}
	}

	mux.HandleFunc("/health", handler(srv.handleHealth))
	mux.HandleFunc("/", handler(srv.handleHealth))
	mux.HandleFunc("/xiaozhi/ota/", handler(srv.handleOta))
	mux.HandleFunc("/xiaozhi/updates", handler(srv.handleUpdates))
	mux.HandleFunc("/xiaozhi/reply", handler(srv.handleReply))
	mux.HandleFunc("/xiaozhi/bind", handler(srv.handleBindRequest))
	mux.HandleFunc("/xiaozhi/bind/confirm", handler(srv.handleBindConfirm))
	mux.HandleFunc("/xiaozhi/bind/status", handler(srv.handleBindStatus))
	mux.HandleFunc("/xiaozhi/v1", handler(srv.handleWebSocket))

	addr := fmt.Sprintf("0.0.0.0:%d", cfg.Port)
	log.Printf("╔══════════════════════════════════════╗")
	log.Printf("║  喵小智 MCP 桥接服务器 v2.1.0     ║")
	log.Printf("╠══════════════════════════════════════╣")
	log.Printf("║  HTTP:    http://0.0.0.0:%d       ║", cfg.Port)
	log.Printf("║  WS:      ws://0.0.0.0:%d/xiaozhi/v1 ║", cfg.Port)
	log.Printf("║  LLM:     %-25s ║", boolToStatus(cfg.LLM.APIKey != ""))
	log.Printf("║  OpenClaw: %-25s ║", boolToStatus(cfg.OpenClaw.Enabled))
	log.Printf("║  Config:  %-25s ║", configPath)
	log.Printf("╚══════════════════════════════════════╝")

	// Graceful shutdown
	go func() {
		sigCh := make(chan os.Signal, 1)
		signal.Notify(sigCh, syscall.SIGINT, syscall.SIGTERM)
		<-sigCh
		log.Println("Shutting down...")
		os.Exit(0)
	}()

	if err := http.ListenAndServe(addr, mux); err != nil {
		log.Fatalf("ListenAndServe: %v", err)
	}
}

func boolToStatus(b bool) string {
	if b {
		return "[已启用]"
	}
	return "[未启用]"
}
