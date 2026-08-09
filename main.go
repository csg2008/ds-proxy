package main

import (
	"bytes"
	"context"
	"encoding/json"
	"flag"
	"io"
	"log"
	"net/http"
	"net/url"
	"os"
	"strconv"
	"time"
)

var (
	targetURL *url.URL
	debug     bool
)

func main() {
	var port string
	flag.StringVar(&port, "port", getEnv("PORT", "15722"), "监听的端口号")
	var host string
	flag.StringVar(&host, "host", getEnv("HOST", "127.0.0.1"), "监听的主体地址")
	var upstream string
	flag.StringVar(&upstream, "upstream", getEnv("DEEPSEEK_HOST", "https://api.deepseek.com"), "上游模型接口地址")
	flag.BoolVar(&debug, "debug", false, "调试开关")
	flag.Parse()

	var err error
	targetURL, err = url.Parse(upstream)
	if err != nil {
		log.Fatalf("Invalid upstream %q: %v", upstream, err)
	}

	http.HandleFunc("/", handleRequest)

	addr := host + ":" + port
	log.Printf("[DeepSeek Proxy] Proxy running at http://%s", addr)
	log.Printf("[Target] %s", upstream)
	log.Fatal(http.ListenAndServe(addr, nil))
}

func getEnv(key, defaultVal string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return defaultVal
}

func handleRequest(w http.ResponseWriter, r *http.Request) {
	// CORS headers
	w.Header().Set("Access-Control-Allow-Origin", "*")
	w.Header().Set("Access-Control-Allow-Methods", "POST, GET, OPTIONS")
	w.Header().Set("Access-Control-Allow-Headers", "Content-Type, Authorization, X-Api-Key, Anthropic-Version")

	if r.Method == http.MethodOptions {
		w.WriteHeader(http.StatusOK)
		return
	}

	// Read original body
	bodyBytes, err := io.ReadAll(r.Body)
	if err != nil {
		log.Printf("[Error] reading body: %v", err)
		writeError(w, http.StatusBadRequest, "Failed to read request body")
		return
	}
	defer r.Body.Close()

	// Parse and sanitize
	var bodyMap map[string]interface{}
	if len(bodyBytes) > 0 {
		if err := json.Unmarshal(bodyBytes, &bodyMap); err != nil {
			log.Printf("[Parse Warning] %v, forwarding original body", err)
			bodyMap = nil
		} else {
			sanitizeMessages(bodyMap)
		}
	}

	// Build modified body
	var forwardBody []byte
	if bodyMap != nil {
		forwardBody, err = json.Marshal(bodyMap)
		if err != nil {
			log.Printf("[Error] marshaling body: %v", err)
			writeError(w, http.StatusInternalServerError, "Failed to process request body")
			return
		}
	} else {
		forwardBody = bodyBytes
	}

	// Build target URL
	targetURLStr := targetURL.String() + r.URL.Path
	if r.URL.RawQuery != "" {
		targetURLStr += "?" + r.URL.RawQuery
	}

	// Create outgoing request with timeout
	ctx, cancel := context.WithTimeout(r.Context(), 120*time.Second)
	defer cancel()

	outReq, err := http.NewRequestWithContext(ctx, r.Method, targetURLStr, bytes.NewReader(forwardBody))
	if err != nil {
		log.Printf("[Error] creating request: %v", err)
		writeError(w, http.StatusInternalServerError, "Failed to create upstream request")
		return
	}

	// Copy headers from original request
	copyHeaders(outReq.Header, r.Header)
	outReq.Header.Set("Content-Length", strconv.Itoa(len(forwardBody)))
	outReq.Host = targetURL.Host

	if debug {
		log.Printf("[Debug] %s %s", r.Method, r.URL.String())
		for k, vv := range r.Header {
			for _, v := range vv {
				log.Printf("[Debug] Request Header: %s: %s", k, v)
			}
		}
		log.Printf("[Debug] Request Body: %s", string(forwardBody))
	}

	// Execute
	client := &http.Client{
		Timeout: 120 * time.Second,
	}
	resp, err := client.Do(outReq)
	if err != nil {
		log.Printf("[Proxy Error] %v", err)
		writeError(w, http.StatusBadGateway, "Upstream connection failed: "+err.Error())
		return
	}
	defer resp.Body.Close()

	// Copy response headers
	copyHeaders(w.Header(), resp.Header)
	w.WriteHeader(resp.StatusCode)

	if debug {
		log.Printf("[Debug] Response Status: %s", resp.Status)
		for k, vv := range resp.Header {
			for _, v := range vv {
				log.Printf("[Debug] Response Header: %s: %s", k, v)
			}
		}
		var respBuf bytes.Buffer
		if _, err := io.Copy(w, io.TeeReader(resp.Body, &respBuf)); err != nil {
			log.Printf("[Error] copying response: %v", err)
		}
		log.Printf("[Debug] Response Body: %s", respBuf.String())
	} else {
		// Stream response body (supports SSE)
		if _, err := io.Copy(w, resp.Body); err != nil {
			log.Printf("[Error] copying response: %v", err)
		}
	}
}

func copyHeaders(dst, src http.Header) {
	for k, vv := range src {
		for _, v := range vv {
			dst.Add(k, v)
		}
	}
}

func writeError(w http.ResponseWriter, code int, message string) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(code)
	json.NewEncoder(w).Encode(map[string]string{
		"error":   http.StatusText(code),
		"message": message,
	})
}

// sanitizeMessages fixes the DeepSeek V4 thinking-mode 400 error.
// DeepSeek requires that assistant messages in thinking mode contain a
// content block with type="thinking". Claude Code drops this field
// when saving conversation history, so we inject an empty one if missing.
func sanitizeMessages(body map[string]interface{}) {
	messagesRaw, ok := body["messages"]
	if !ok {
		return
	}

	messages, ok := messagesRaw.([]interface{})
	if !ok {
		return
	}

	for _, msgRaw := range messages {
		msg, ok := msgRaw.(map[string]interface{})
		if !ok {
			continue
		}

		role, _ := msg["role"].(string)
		if role != "assistant" {
			continue
		}

		content, ok := msg["content"]
		if !ok {
			continue
		}

		switch v := content.(type) {
		case []interface{}:
			// Anthropic format: content is an array of blocks
			hasThinking := false
			for _, blockRaw := range v {
				block, ok := blockRaw.(map[string]interface{})
				if !ok {
					continue
				}
				if t, _ := block["type"].(string); t == "thinking" {
					hasThinking = true
					break
				}
			}

			if !hasThinking {
				emptyThinking := map[string]interface{}{
					"type":      "thinking",
					"thinking":  "",
					"signature": "",
				}
				newContent := make([]interface{}, 0, len(v)+1)
				newContent = append(newContent, emptyThinking)
				newContent = append(newContent, v...)
				msg["content"] = newContent
			}

		case string:
			// OpenAI compatible format
			_ = v
			if _, exists := msg["reasoning_content"]; !exists {
				msg["reasoning_content"] = ""
			}
		}
	}
}
