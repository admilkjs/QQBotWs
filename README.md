# QQBotWs - QQ Bot WebSocket 代理服务

## 项目简介

## 🌌 虚空通信枢纽

双语言实现的跨次元通信协议核心，提供：

- 🧬 Go/TypeScript 双生契约刻印
- 🔁 自动时空重构术式（最大 10 次逆召唤）
- 🗡️ 多维度编译矩阵
- 🔒 深渊加密结界（HTTPS）
- 📡 灵能波动监控阵列

## ⚡ 元素召唤矩阵

- 🔥 双向星界之门（WebSocket）
- 🌀 混沌数据解压术（GZIP/DEFLATE/Brotli）
- 🧿 多重镜像次元（appid 隔离）
- 🌐 跨界通行证（CORS 配置）
- ⏳ 实时虚空回响监测

## 多平台支持

| 操作系统 | 架构  | 构建产物名称              |
| -------- | ----- | ------------------------- |
| Linux    | amd64 | QQBotWs-linux-amd64       |
| Linux    | arm64 | QQBotWs-linux-arm64       |
| Windows  | amd64 | QQBotWs-windows-amd64.exe |
| macOS    | arm64 | QQBotWs-darwin-arm64      |

## HTTPS 配置

1. 生成证书：

[点我生成证书](https://bdfy.azurewebsites.net/?%E6%80%8E%E4%B9%88%E7%94%9F%E6%88%90ssl%E8%AF%81%E4%B9%A6)

2. 启动参数：

```bash
./QQBotWs-linux-amd64
```

## 环境变量

| 变量名    | 默认值 | 说明                            |
| --------- | ------ | ------------------------------- |
| PORT      | 2173   | 服务监听端口                    |
| LOG_LEVEL | info   | 日志级别(debug/info/warn/error) |
| HTTPS     | false  | 是否启用 HTTPS                  |

## 健康检查接口

```http
GET /health
```

响应示例：

```json
{
	"status": "ok",
	"connections": 5
}
```


## 🧙♂️ 暗夜运行指南

### 🔮 魔导书目录（证书存放）

```
📂 项目根目录
├── 📜 cert.pem    # 神圣加密契约
└── 📜 key.pem     # 深渊秘钥石板
```

### ⚡ PM2 守护仪式

```bash
# 安装暗影仆从
npm install pm2 -g

# 启动永夜结界
pm2 start QQBotWs-linux-amd64 --name "dark-bot" -- \
  --port=3000 \
  --HTTPS=true \

# 查看契约铭文
pm2 logs dark-bot
```

### 🌌 虚空召唤阵（systemd 配置）

```ini
[Unit]
Description=Dark WebSocket Daemon

[Service]
ExecStart=/path/to/QQBotWs-linux-amd64 \
  --port=3000 \
  --HTTPS=true \
  --log-level=info
Restart=always

[Install]
WantedBy=multi-user.target
```

## ⚡ 混沌交流圣域

```
💬 QQ群契约印记：792873018
📡 加群暗号：「来自虚空低语者」
```

## 开源协议

本项目采用 [AGPL-3.0](LICENSE) 协议开源
