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
	"log"
	"net/http"
	"net/url"
	"os"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/gorilla/websocket"
)

type QQBot struct {
	self           *websocket.Conn
	target         *websocket.Conn
	appid          int64
	url            string
	reconnectMu    sync.Mutex
	reconnectCount int
	maxRetries     int
	retryDelay     time.Duration
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
		deflateReader := flate.NewReader(bytes.NewReader(buffer))
		defer deflateReader.Close()
		reader = deflateReader
	case "br":
		reader = bzip2.NewReader(bytes.NewReader(buffer))
	default:
		log.Printf("[HTTP] 不支持的压缩格式: %s", encoding)
		return buffer, nil
	}

	decompressed, err := io.ReadAll(reader)
	if err != nil {
		log.Printf("[HTTP] 解压失败: %v", err)
		return buffer, err
	}

	return decompressed, nil
}

func (bot *QQBot) cleanup() {
	bot.reconnectMu.Lock()
	defer bot.reconnectMu.Unlock()

	if bot.target != nil {
		bot.target.Close()
		bot.target = nil
	}

	appidStr := strconv.FormatInt(bot.appid, 10)
	if conns, ok := userConnections.Load(appidStr); ok {
		connList := conns.([]*QQBot)
		updatedList := make([]*QQBot, 0)
		for _, conn := range connList {
			if conn != bot {
				updatedList = append(updatedList, conn)
			}
		}
		if len(updatedList) > 0 {
			userConnections.Store(appidStr, updatedList)
		} else {
			userConnections.Delete(appidStr)
		}
		log.Printf("[WS] 连接清理完成 appid:%d 剩余连接:%d", bot.appid, len(updatedList))
	}

	var count int
	userConnections.Range(func(key, value interface{}) bool {
		count++
		return true
	})
	log.Printf("[WS] 当前活跃应用数:%d", count)
}

func (bot *QQBot) handleReconnect() error {
	bot.reconnectMu.Lock()
	defer bot.reconnectMu.Unlock()

	if bot.reconnectCount >= bot.maxRetries {
		log.Printf("[WS] 达到最大重试次数 appid:%d", bot.appid)
		return fmt.Errorf("达到最大重试次数")
	}

	bot.reconnectCount++
	log.Printf("[WS] 尝试重连 appid:%d 次数:%d/%d", bot.appid, bot.reconnectCount, bot.maxRetries)
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
		log.Printf("[WS] 创建目标连接失败 appid:%d error:%v", bot.appid, err)
		return err
	}

	bot.target = targetConn
	log.Printf("[WS] 目标连接成功 appid:%d", bot.appid)
	bot.reconnectCount = 0

	go bot.readTarget()
	return nil
}

func (bot *QQBot) readTarget() {
	for {
		_, message, err := bot.target.ReadMessage()
		if err != nil {
			log.Printf("[WS] 目标连接关闭 appid:%d error:%v", bot.appid, err)
			if err := bot.handleReconnect(); err != nil {
				bot.self.Close()
				bot.cleanup()
			}
			break
		}

		log.Printf("[WS] 收到目标消息 appid:%d 长度:%d", bot.appid, len(message))
		if err := bot.self.WriteMessage(websocket.TextMessage, message); err != nil {
			log.Printf("[WS] 转发目标消息失败 appid:%d error:%v", bot.appid, err)
			break
		}
		log.Printf("[WS] 转发目标消息 appid:%d 状态:成功 长度:%d", bot.appid, len(message))
	}
}

func (bot *QQBot) readSelf() {
	for {
		_, message, err := bot.self.ReadMessage()
		if err != nil {
			log.Printf("[WS] 客户端关闭连接 appid:%d error:%v", bot.appid, err)
			bot.cleanup()
			break
		}

		log.Printf("[WS] 收到客户端消息 appid:%d 长度:%d", bot.appid, len(message))
		if bot.target != nil {
			if err := bot.target.WriteMessage(websocket.TextMessage, message); err != nil {
				log.Printf("[WS] 转发客户端消息失败 appid:%d error:%v", bot.appid, err)
				continue
			}
			log.Printf("[WS] 转发客户端消息 appid:%d 状态:成功 长度:%d", bot.appid, len(message))
		} else {
			log.Printf("[WS] 转发客户端消息失败 appid:%d 原因:目标连接未就绪", bot.appid)
		}
	}
}

func handleProxy(w http.ResponseWriter, r *http.Request) {
	queryURL := r.URL.Query().Get("url")
	if queryURL == "" {
		log.Print("[HTTP] 代理请求缺少URL参数")
		http.Error(w, `{"error":"Missing URL"}`, http.StatusBadRequest)
		return
	}

	targetURL, err := url.Parse(queryURL)
	if err != nil {
		log.Printf("[HTTP] URL解析失败: %v", err)
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

	log.Printf("[HTTP] 开始代理请求 %s %s", r.Method, targetURL.String())

	client := &http.Client{}
	proxyReq, err := http.NewRequest(r.Method, targetURL.String(), r.Body)
	if err != nil {
		log.Printf("[HTTP] 创建代理请求失败: %v", err)
		http.Error(w, `{"error":"Proxy error"}`, http.StatusInternalServerError)
		return
	}

	// 复制请求头
	proxyReq.Header.Set("User-Agent", "BotNodeSDK/0.0.1")
	proxyReq.Header.Set("Accept-Encoding", "gzip, deflate, br")
	if auth := r.Header.Get("Authorization"); auth != "" {
		proxyReq.Header.Set("Authorization", auth)
	}
	if appID := r.Header.Get("X-Union-Appid"); appID != "" {
		proxyReq.Header.Set("X-Union-Appid", appID)
	}
	if contentType := r.Header.Get("Content-Type"); contentType != "" {
		proxyReq.Header.Set("Content-Type", contentType)
	}

	startTime := time.Now()
	resp, err := client.Do(proxyReq)
	if err != nil {
		log.Printf("[HTTP] 代理请求失败: %v", err)
		http.Error(w, `{"error":"Proxy error"}`, http.StatusInternalServerError)
		return
	}
	defer resp.Body.Close()

	log.Printf("[HTTP] 代理响应 %s 状态:%d 耗时:%v", targetURL.String(), resp.StatusCode, time.Since(startTime))

	// 读取响应体
	responseBody, err := ioutil.ReadAll(resp.Body)
	if err != nil {
		log.Printf("[HTTP] 读取响应体失败: %v", err)
		http.Error(w, `{"error":"Proxy error"}`, http.StatusInternalServerError)
		return
	}

	// 处理压缩内容
	if encoding := resp.Header.Get("Content-Encoding"); encoding != "" {
		log.Printf("[HTTP] 检测到压缩内容 编码格式:%s", encoding)
		decompressed, err := decompressBuffer(responseBody, encoding)
		if err == nil {
			responseBody = decompressed
			log.Printf("[HTTP] 解压成功 原始长度:%d", len(responseBody))
		} else {
			log.Printf("[HTTP] 解压失败，返回原始数据: %v", err)
		}
	}

	// 设置响应头
	for k, v := range resp.Header {
		if !strings.EqualFold(k, "Content-Length") &&
			!strings.EqualFold(k, "Transfer-Encoding") &&
			!strings.EqualFold(k, "Content-Encoding") {
			w.Header()[k] = v
		}
	}

	w.Header().Set("Content-Length", strconv.Itoa(len(responseBody)))
	w.WriteHeader(resp.StatusCode)
	w.Write(responseBody)
}

func handleWebSocket(w http.ResponseWriter, r *http.Request) {
	appidStr := r.URL.Query().Get("appid")
	queryURL := r.URL.Query().Get("url")

	if appidStr == "" || queryURL == "" {
		log.Printf("[WS] 非法连接参数 appid:%s url:%s", appidStr, queryURL)
		http.Error(w, "Invalid parameters", http.StatusBadRequest)
		return
	}

	appid, err := strconv.ParseInt(appidStr, 10, 64)
	if err != nil {
		log.Printf("[WS] 非法appid格式: %v", err)
		http.Error(w, "Invalid appid", http.StatusBadRequest)
		return
	}

	parsedURL, err := url.Parse(queryURL)
	if err != nil {
		log.Printf("[WS] 非法URL格式: %v", err)
		http.Error(w, "Invalid URL", http.StatusBadRequest)
		return
	}
	_ = parsedURL

	conn, err := upgrader.Upgrade(w, r, nil)
	if err != nil {
		log.Printf("[WS] WebSocket升级失败: %v", err)
		return
	}

	bot := &QQBot{
		self:       conn,
		appid:      appid,
		url:        queryURL,
		maxRetries: 10,
		retryDelay: 5 * time.Second,
	}

	if err := bot.connectTarget(); err != nil {
		conn.Close()
		return
	}

	conns, _ := userConnections.LoadOrStore(appidStr, []*QQBot{})
	connList := append(conns.([]*QQBot), bot)
	userConnections.Store(appidStr, connList)

	log.Printf("[WS] 当前连接数 appid:%s 数量:%d", appidStr, len(connList))

	go bot.readSelf()
}

func main() {
	// 加载证书
	cert, err := tls.LoadX509KeyPair("cert.pem", "key.pem")
	if err != nil {
		log.Fatalf("[SERVER] 加载证书失败: %v", err)
	}

	// 配置TLS
	config := &tls.Config{
		Certificates: []tls.Certificate{cert},
	}

	// 创建HTTPS服务器
	server := &http.Server{
		Addr:      ":3000",
		TLSConfig: config,
	}

	// 注册路由
	http.HandleFunc("/proxy", handleProxy)
	http.HandleFunc("/ws", handleWebSocket)
	http.HandleFunc("/health", func(w http.ResponseWriter, r *http.Request) {
		var count int
		userConnections.Range(func(key, value interface{}) bool {
			count++
			return true
		})
		json.NewEncoder(w).Encode(map[string]interface{}{"status": "ok", "connections": count})
	})

	// 设置CORS中间件
	handler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Access-Control-Allow-Origin", "*")
		w.Header().Set("Access-Control-Allow-Methods", "*")
		w.Header().Set("Access-Control-Allow-Headers", "*")

		if r.Method == "OPTIONS" {
			log.Printf("[HTTP] 处理OPTIONS请求 %s", r.URL.Path)
			w.WriteHeader(http.StatusNoContent)
			return
		}

		http.DefaultServeMux.ServeHTTP(w, r)
	})

	server.Handler = handler

	port := os.Getenv("PORT")
	if port == "" {
		port = "3000"
	}
	log.Printf("[SERVER] 服务器已启动 监听地址::%s", port)

	if err := server.ListenAndServeTLS("", ""); err != nil {
		log.Fatalf("[SERVER] 服务器启动失败: %v", err)
	}
}
