package main

import (
	"bytes"
	"compress/bzip2"
	"compress/flate"
	"compress/gzip"
	"crypto/tls"
	"encoding/json"
	"fmt"
	"io"
	"io/ioutil"
	"net/http"
	"net/url"
	"os"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/gorilla/websocket"
	log "github.com/sirupsen/logrus"
)

func init() {

	levelStr := strings.ToLower(os.Getenv("LOG_LEVEL"))
	level, err := log.ParseLevel(levelStr)
	if err != nil {
		level = log.InfoLevel
	}
	log.SetLevel(level)
	log.SetFormatter(&log.TextFormatter{
		FullTimestamp: true,
	})
}

type QQBot struct {
	self           *websocket.Conn
	target         *websocket.Conn
	appid          int64
	url            string
	reconnectMu    sync.Mutex
	reconnectCount int
	maxRetries     int
	retryDelay     time.Duration
	active         bool
}

var (
	userConnections = sync.Map{}
	upgrader        = websocket.Upgrader{
		CheckOrigin: func(r *http.Request) bool { return true },
	}
)

func decompressBuffer(buffer []byte, encoding string) ([]byte, error) {
	var reader io.Reader

	switch encoding {
	case "gzip":
		gr, err := gzip.NewReader(bytes.NewReader(buffer))
		if err != nil {
			return nil, err
		}
		defer gr.Close()
		reader = gr
	case "deflate":
		dr := flate.NewReader(bytes.NewReader(buffer))
		defer dr.Close()
		reader = dr
	case "br":
		reader = bzip2.NewReader(bytes.NewReader(buffer))
	default:
		log.Warnf("[HTTP] 不支持的压缩格式: %s", encoding)
		return buffer, nil
	}

	decompressed, err := io.ReadAll(reader)
	if err != nil {
		log.Errorf("[HTTP] 解压失败: %v", err)
		return buffer, err
	}

	return decompressed, nil
}

func (bot *QQBot) cleanup() {
	bot.reconnectMu.Lock()
	defer bot.reconnectMu.Unlock()

	bot.active = false

	if bot.target != nil {
		bot.target.Close()
		bot.target = nil
	}

	appidStr := strconv.FormatInt(bot.appid, 10)
	if conns, ok := userConnections.Load(appidStr); ok {
		connList := conns.([]*QQBot)
		updated := make([]*QQBot, 0, len(connList))
		for _, c := range connList {
			if c != bot {
				updated = append(updated, c)
			}
		}
		if len(updated) > 0 {
			userConnections.Store(appidStr, updated)
		} else {
			userConnections.Delete(appidStr)
		}
		log.Infof("[WS] 连接清理完成 appid:%d 剩余连接:%d", bot.appid, len(updated))
	}

	var count int
	userConnections.Range(func(_, _ interface{}) bool {
		count++
		return true
	})
	log.Infof("[WS] 当前活跃应用数:%d", count)
}

func (bot *QQBot) handleReconnect() error {
	bot.reconnectMu.Lock()
	defer bot.reconnectMu.Unlock()

	if !bot.active {
		log.Infof("[WS] Bot inactive，不再重连 appid:%d", bot.appid)
		return fmt.Errorf("bot inactive")
	}
	if bot.reconnectCount >= bot.maxRetries {
		log.Warnf("[WS] 达到最大重试次数 appid:%d", bot.appid)
		return fmt.Errorf("达到最大重试次数")
	}

	bot.reconnectCount++
	log.Infof("[WS] 尝试重连 appid:%d 次数:%d/%d", bot.appid, bot.reconnectCount, bot.maxRetries)
	time.Sleep(bot.retryDelay)

	return bot.connectTarget()
}

func (bot *QQBot) connectTarget() error {

	if bot.target != nil {
		bot.target.Close()
		bot.target = nil
	}

	dialer := websocket.Dialer{}
	targetConn, _, err := dialer.Dial(bot.url, nil)
	if err != nil {
		log.Errorf("[WS] 创建目标连接失败 appid:%d error:%v", bot.appid, err)
		return err
	}

	bot.target = targetConn
	bot.reconnectCount = 0
	log.Infof("[WS] 目标连接成功 appid:%d", bot.appid)

	go bot.readTarget()
	return nil
}

func (bot *QQBot) readTarget() {
	for {
		_, message, err := bot.target.ReadMessage()
		if err != nil {
			log.Warnf("[WS] 目标连接关闭 appid:%d error:%v", bot.appid, err)
			if err := bot.handleReconnect(); err != nil {
				bot.self.Close()
				bot.cleanup()
			}
			break
		}

		log.Infof("[WS] 收到目标消息 appid:%d 长度:%d", bot.appid, len(message))
		if err := bot.self.WriteMessage(websocket.TextMessage, message); err != nil {
			log.Errorf("[WS] 转发目标消息失败 appid:%d error:%v", bot.appid, err)
			break
		}
		log.Infof("[WS] 转发目标消息成功 appid:%d 长度:%d", bot.appid, len(message))

		var parsed map[string]any
		if err := json.Unmarshal(message, &parsed); err != nil {
			log.Debugf("[WS] JSON解析失败 appid:%d error:%v", bot.appid, err)
		} else if data, err := json.Marshal(parsed); err == nil {
			log.Debugf("[WS] 目标消息内容: %s", string(data))
		}
	}
}

func (bot *QQBot) readSelf() {
	for {
		_, message, err := bot.self.ReadMessage()
		if err != nil {
			log.Warnf("[WS] 客户端关闭连接 appid:%d error:%v", bot.appid, err)
			bot.cleanup()
			break
		}

		log.Infof("[WS] 收到客户端消息 appid:%d 长度:%d", bot.appid, len(message))
		if bot.target != nil {
			if err := bot.target.WriteMessage(websocket.TextMessage, message); err != nil {
				log.Errorf("[WS] 转发客户端消息失败 appid:%d error:%v", bot.appid, err)
				continue
			}
			log.Infof("[WS] 转发客户端消息成功 appid:%d 长度:%d", bot.appid, len(message))

			var parsed map[string]any
			if err := json.Unmarshal(message, &parsed); err != nil {
				log.Debugf("[WS] JSON解析失败 appid:%d error:%v", bot.appid, err)
			} else if data, err := json.Marshal(parsed); err == nil {
				log.Debugf("[WS] 客户端消息内容: %s", string(data))
			}
		} else {
			log.Warnf("[WS] 转发客户端消息失败 appid:%d 原因:目标连接未就绪", bot.appid)
		}
	}
}

func handleProxy(w http.ResponseWriter, r *http.Request) {
	queryURL := r.URL.Query().Get("url")
	if queryURL == "" {
		log.Warn("[HTTP] 代理请求缺少URL参数")
		http.Error(w, `{"error":"Missing URL"}`, http.StatusBadRequest)
		return
	}

	targetURL, err := url.Parse(queryURL)
	if err != nil {
		log.Errorf("[HTTP] URL解析失败: %v", err)
		http.Error(w, `{"error":"Invalid URL"}`, http.StatusBadRequest)
		return
	}

	q := targetURL.Query()
	for k, v := range r.URL.Query() {
		if k != "url" {
			q.Set(k, v[0])
		}
	}
	targetURL.RawQuery = q.Encode()

	log.Infof("[HTTP] 开始代理请求 %s %s", r.Method, targetURL.String())

	client := &http.Client{}
	proxyReq, err := http.NewRequest(r.Method, targetURL.String(), r.Body)
	if err != nil {
		log.Errorf("[HTTP] 创建代理请求失败: %v", err)
		http.Error(w, `{"error":"Proxy error"}`, http.StatusInternalServerError)
		return
	}

	proxyReq.Header.Set("User-Agent", "BotNodeSDK/0.0.1")
	proxyReq.Header.Set("Accept-Encoding", "gzip, deflate, br")
	if auth := r.Header.Get("Authorization"); auth != "" {
		proxyReq.Header.Set("Authorization", auth)
	}
	if appID := r.Header.Get("X-Union-Appid"); appID != "" {
		proxyReq.Header.Set("X-Union-Appid", appID)
	}
	if ct := r.Header.Get("Content-Type"); ct != "" {
		proxyReq.Header.Set("Content-Type", ct)
	}

	start := time.Now()
	resp, err := client.Do(proxyReq)
	if err != nil {
		log.Errorf("[HTTP] 代理请求失败: %v", err)
		http.Error(w, `{"error":"Proxy error"}`, http.StatusInternalServerError)
		return
	}
	defer resp.Body.Close()

	log.Infof("[HTTP] 代理响应 %s 状态:%d 耗时:%v", targetURL.String(), resp.StatusCode, time.Since(start))

	body, err := ioutil.ReadAll(resp.Body)
	if err != nil {
		log.Errorf("[HTTP] 读取响应体失败: %v", err)
		http.Error(w, `{"error":"Proxy error"}`, http.StatusInternalServerError)
		return
	}

	if enc := resp.Header.Get("Content-Encoding"); enc != "" {
		log.Infof("[HTTP] 检测到压缩内容 编码格式:%s", enc)
		if dec, err := decompressBuffer(body, enc); err == nil {
			body = dec
			log.Infof("[HTTP] 解压成功 原始长度:%d", len(body))
		} else {
			log.Warnf("[HTTP] 解压失败，返回原始数据: %v", err)
		}
	}

	for k, v := range resp.Header {
		if strings.EqualFold(k, "Content-Length") ||
			strings.EqualFold(k, "Transfer-Encoding") ||
			strings.EqualFold(k, "Content-Encoding") {
			continue
		}
		w.Header()[k] = v
	}

	w.Header().Set("Content-Length", strconv.Itoa(len(body)))
	w.WriteHeader(resp.StatusCode)
	w.Write(body)
}

func handleWebSocket(w http.ResponseWriter, r *http.Request) {
	appidStr := r.URL.Query().Get("appid")
	queryURL := r.URL.Query().Get("url")
	if appidStr == "" || queryURL == "" {
		log.Warnf("[WS] 非法连接参数 appid:%s url:%s", appidStr, queryURL)
		http.Error(w, "Invalid parameters", http.StatusBadRequest)
		return
	}
	appid, err := strconv.ParseInt(appidStr, 10, 64)
	if err != nil {
		log.Errorf("[WS] 非法appid格式: %v", err)
		http.Error(w, "Invalid appid", http.StatusBadRequest)
		return
	}
	if _, err := url.Parse(queryURL); err != nil {
		log.Errorf("[WS] 非法URL格式: %v", err)
		http.Error(w, "Invalid URL", http.StatusBadRequest)
		return
	}

	conn, err := upgrader.Upgrade(w, r, nil)
	if err != nil {
		log.Errorf("[WS] WebSocket升级失败: %v", err)
		return
	}

	bot := &QQBot{
		self:       conn,
		appid:      appid,
		url:        queryURL,
		maxRetries: 10,
		retryDelay: 5 * time.Second,
		active:     true,
	}

	if err := bot.connectTarget(); err != nil {
		conn.Close()
		return
	}

	conns, _ := userConnections.LoadOrStore(appidStr, []*QQBot{})
	list := append(conns.([]*QQBot), bot)
	userConnections.Store(appidStr, list)
	log.Infof("[WS] 当前连接数 appid:%s 数量:%d", appidStr, len(list))

	go bot.readSelf()
}

func main() {
	port := os.Getenv("PORT")
	if port == "" {
		port = "2173"
	}
	ishttps := os.Getenv("HTTPS")
	if ishttps == "" {
		// _, err := os.Stat("cert.pem")
		// if os.IsNotExist(err) {
		// 	log.Fatal("[SERVER] 未找到证书文件 cert.pem,启用HTTP模式")
		// 	ishttps = "false"

		// } else {
		ishttps = "false"
		// }
	}
	var server *http.Server
	if ishttps == "true" {
		cert, err := tls.LoadX509KeyPair("cert.pem", "key.pem")
		if err != nil {
			log.Fatalf("[SERVER] 加载证书失败: %v", err)
			return
		}

		server = &http.Server{
			Addr: ":" + port,
			TLSConfig: &tls.Config{
				Certificates: []tls.Certificate{cert},
			},
		}
	} else {
		server = &http.Server{
			Addr: ":" + port,
		}
	}
	http.HandleFunc("/proxy", handleProxy)
	http.HandleFunc("/ws", handleWebSocket)
	http.HandleFunc("/health", func(w http.ResponseWriter, r *http.Request) {
		var count int
		userConnections.Range(func(_, _ interface{}) bool {
			count++
			return true
		})
		var appids []string
		userConnections.Range(func(key, _ interface{}) bool {
			if keyStr, ok := key.(string); ok {
				appids = append(appids, keyStr)
			}
			return true
		})
		json.NewEncoder(w).Encode(map[string]interface{}{
			"status":      "ok",
			"connections": count,
			"appids":      appids,
		})
	})

	handler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Access-Control-Allow-Origin", "*")
		w.Header().Set("Access-Control-Allow-Methods", "*")
		w.Header().Set("Access-Control-Allow-Headers", "*")
		if r.Method == "OPTIONS" {
			log.Infof("[HTTP] 处理OPTIONS请求 %s", r.URL.Path)
			w.WriteHeader(http.StatusNoContent)
			return
		}
		http.DefaultServeMux.ServeHTTP(w, r)
	})
	server.Handler = handler

	if ishttps == "true" {
		log.Infof("[SERVER] 启动HTTPS服务器，端口:%s", port)
	} else {
		log.Infof("[SERVER] 启动HTTP服务器，端口:%s", port)
	}
	if ishttps == "true" {
		if err := server.ListenAndServeTLS("", ""); err != nil {
			log.Fatalf("[SERVER] 启动HTTPS服务器失败: %v", err)
		}
		defer server.Close()
	} else {
		if err := server.ListenAndServe(); err != nil {
			log.Fatalf("[SERVER] 启动HTTP服务器失败: %v", err)
		}
		defer server.Close()
	}
}
