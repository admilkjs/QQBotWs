import Fastify from 'fastify'
import FastifyWebsocket from '@fastify/websocket'
import fs from 'node:fs'
import { WebSocket } from 'ws'

interface QueryParams {
	appid: string
	url: string
}

class QQBot {
	private self: WebSocket
	private target: WebSocket | null = null
	private appid: number
	private url: string
	private reconnectAttempts = 0
	private readonly MAX_RECONNECT_ATTEMPTS = 10
	private readonly RECONNECT_DELAY = 5000

	constructor(connect: WebSocket, appid: number, url: string) {
		this.self = connect
		this.appid = appid
		this.url = url
		this.initTarget()
		this.self.on('message', (data) => {
			if (this.target?.readyState === WebSocket.OPEN) {
				this.target.send(data)
			}
		})

		this.self.on('close', () => {
			console.log(`客户端断开连接，appid: ${this.appid}`)

			if (this.target?.readyState === WebSocket.OPEN) {
				this.target.close()
			}

			this.removeFromConnectionMap()
		})

		this.self.on('error', (error) => {
			console.error(`Yunzai端WebSocket错误，appid ${this.appid}:`, error)
		})
	}

	private initTarget(): void {
		try {
			this.target = new WebSocket(this.url)

			this.target.on('open', () => {
				console.log(`成功建立与 ${this.url} 的连接，appid: ${this.appid}`)
				this.reconnectAttempts = 0
			})

			this.target.on('message', (data) => {
				if (this.self.readyState === WebSocket.OPEN) {
					this.self.send(data)
				}
			})

			this.target.on('close', () => this.handleTargetClose())

			this.target.on('error', (error) => {
				console.error(`目标WebSocket错误，appid ${this.appid}:`, error)
			})
		} catch (error) {
			console.error(`初始化连接失败，appid ${this.appid}:`, error)
			this.self.close(1011, '服务器初始化连接时出错')
		}
	}

	private async handleTargetClose(): Promise<void> {
		if (this.reconnectAttempts >= this.MAX_RECONNECT_ATTEMPTS) {
			console.error(`达到最大重连次数，appid ${this.appid}`)
			this.self.close(1011, '目标服务不可用')
			return
		}

		this.reconnectAttempts++
		console.log(
			`尝试重新连接到 ${this.url}（第 ${this.reconnectAttempts}/${this.MAX_RECONNECT_ATTEMPTS} 次尝试）`,
		)

		await new Promise((resolve) => setTimeout(resolve, this.RECONNECT_DELAY))
		this.initTarget()
	}

	private removeFromConnectionMap(): void {
		const connections = userConnections.get(this.appid.toString()) || []
		const index = connections.indexOf(this)

		if (index !== -1) {
			connections.splice(index, 1)

			if (connections.length === 0) {
				userConnections.delete(this.appid.toString())
			} else {
				userConnections.set(this.appid.toString(), connections)
			}
		}

		console.log(`appid ${this.appid} 的连接已关闭`)
		console.log(`剩余活跃appid数量: ${userConnections.size}`)
	}
}

const userConnections = new Map<string, QQBot[]>()

async function startServer() {
	try {
		const app = Fastify({
			https: {
				key: fs.readFileSync('./key.pem'),
				cert: fs.readFileSync('./cert.pem'),
			},
			logger: true,
		})

		await app.register(FastifyWebsocket)

		app.get('/', { websocket: true }, (connection, request) => {
			try {
				const socket = connection
				const { query } = request
				const { appid, url } = query as QueryParams

				if (!appid || !url) {
					socket.close(1003, '缺少必要参数')
					return
				}

				try {
					new URL(url)
				} catch {
					socket.close(1003, 'URL格式无效')
					return
				}

				const bot = new QQBot(socket, Number(appid), url)

				const connections = userConnections.get(appid) || []
				connections.push(bot)
				userConnections.set(appid, connections)

				console.log(`appid ${appid} 建立了新连接`)
				console.log(`当前活跃appid总数: ${userConnections.size}`)
			} catch (error) {
				console.error('处理WebSocket连接时出错:', error)
				connection.close(1011, '服务器错误')
			}
		})

		const port = process.env.PORT ? parseInt(process.env.PORT, 10) : 3000
		const address = await app.listen({ port, host: '0.0.0.0' })
		console.log(`服务器启动，监听地址: ${address}`)
	} catch (error) {
		console.error('服务器启动失败:', error)
		process.exit(1)
	}
}

startServer()
