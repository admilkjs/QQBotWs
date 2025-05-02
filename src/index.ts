import Fastify from 'fastify'
import FastifyWebsocket from '@fastify/websocket'
import fs from 'node:fs'
import { WebSocket } from 'ws'
import zlib from 'zlib'

async function decompressBuffer(
	buffer: Buffer,
	encoding: string,
): Promise<Buffer> {
	return new Promise((resolve) => {
		const decompressor =
			encoding === 'gzip'
				? zlib.createGunzip()
				: encoding === 'deflate'
				? zlib.createInflateRaw() // 修复：使用原始 Inflate 处理 deflate
				: encoding === 'br'
				? zlib.createBrotliDecompress()
				: null

		if (!decompressor) {
			console.error(`[HTTP] 不支持的压缩格式: ${encoding}`)
			return resolve(buffer)
		}

		const chunks: Buffer[] = []
		decompressor
			.on('data', (chunk) => chunks.push(chunk))
			.on('end', () => resolve(Buffer.concat(chunks)))
			.on('error', (err) => {
				console.error(`[HTTP] 解压失败: ${err.message}`)
				resolve(buffer) // 修复：解压失败时返回原始数据
			})

		decompressor.write(buffer)
		decompressor.end()
	})
}

interface QueryParams {
	appid: string
	url: string
}

class QQBot {
	private self: WebSocket
	private target: WebSocket | null = null
	private appid: number
	private url: string
	private reconnectCount = 0
	private maxRetries = 10
	private retryDelay = 5000

	constructor(connect: WebSocket, appid: number, url: string) {
		this.self = connect
		this.appid = appid
		this.url = url
		console.log(`[WS] 新建连接 appid:${appid} 目标:${url}`)
		this.setupSelf()
		this.connectTarget()
	}

	private setupSelf() {
		this.self.on('message', (data) => {
			console.debug(
				`[WS] 收到客户端消息 appid:${this.appid} 长度:${
					data.toString().length
				}`,
			)

			if (this.target?.readyState === WebSocket.OPEN) {
				console.log(
					`[WS] 转发客户端消息 appid:${this.appid} 状态:成功 长度:${
						data.toString().length
					}`,
				)
				this.target.send(data)
			} else {
				console.warn(
					`[WS] 转发客户端消息失败 appid:${this.appid} 原因:目标连接未就绪 当前状态:${this.target?.readyState}`,
				)
			}
		})

		this.self.on('close', (code, reason) => {
			console.log(
				`[WS] 客户端关闭连接 appid:${this.appid} 代码:${code} 原因:${reason}`,
			)
			this.target?.close()
			this.cleanup()
		})

		this.self.on('error', (err) => {
			console.error(`[WS] 客户端错误 appid:${this.appid}`, err)
			this.self.close()
		})
	}

	private connectTarget() {
		this.cleanupTarget()

		try {
			console.log(`[WS] 正在连接目标服务 appid:${this.appid} url:${this.url}`)
			this.target = new WebSocket(this.url)
			this.setupTargetEvents()
		} catch (error) {
			console.error(`[WS] 创建目标连接失败 appid:${this.appid}`, error)
			this.self.close(1011, 'Connection failed')
		}
	}

	private setupTargetEvents() {
		if (!this.target) return

		this.target.on('open', () => {
			console.log(`[WS] 目标连接成功 appid:${this.appid}`)
			this.reconnectCount = 0
		})

		this.target.on('message', (data) => {
			console.debug(
				`[WS] 收到目标消息 appid:${this.appid} 长度:${data.toString().length}`,
			)

			if (this.self.readyState === WebSocket.OPEN) {
				console.log(
					`[WS] 转发目标消息 appid:${this.appid} 状态:成功 长度:${
						data.toString().length
					}`,
				)
				this.self.send(data)
			} else {
				console.warn(
					`[WS] 转发目标消息失败 appid:${this.appid} 原因:客户端连接已关闭 当前状态:${this.self.readyState}`,
				)
			}
		})

		this.target.on('close', (code, reason) => {
			console.log(
				`[WS] 目标连接关闭 appid:${this.appid} 代码:${code} 原因:${reason}`,
			)
			this.handleReconnect()
		})

		this.target.on('error', (err) => {
			console.error(`[WS] 目标连接错误 appid:${this.appid}`, err)
			this.target?.close()
		})
	}

	private async handleReconnect() {
		if (this.reconnectCount++ >= this.maxRetries) {
			console.error(`[WS] 达到最大重试次数 appid:${this.appid}`)
			return this.self.close(1011, 'Service unavailable')
		}

		console.log(
			`[WS] 尝试重连 appid:${this.appid} 次数:${this.reconnectCount}/${this.maxRetries}`,
		)
		await new Promise((r) => setTimeout(r, this.retryDelay))
		this.connectTarget()
	}

	private cleanupTarget() {
		if (!this.target) return

		console.debug(`[WS] 清理目标连接 appid:${this.appid}`)
		this.target.removeAllListeners()
		this.target.readyState === WebSocket.OPEN && this.target.close()
		this.target = null
	}

	private cleanup() {
		const connections = userConnections.get(this.appid.toString()) || []
		const updated = connections.filter((c) => c !== this)

		updated.length
			? userConnections.set(this.appid.toString(), updated)
			: userConnections.delete(this.appid.toString())

		console.log(
			`[WS] 连接清理完成 appid:${this.appid} 剩余连接:${updated.length}`,
		)
		console.log(`[WS] 当前活跃应用数:${userConnections.size}`)
		this.cleanupTarget()
	}
}

const userConnections = new Map<string, QQBot[]>()

async function startServer() {
	const app = Fastify({
		https: {
			key: fs.readFileSync('./key.pem'),
			cert: fs.readFileSync('./cert.pem'),
		},
		logger: false,
		maxParamLength: 1000,
	})

	app.addContentTypeParser(
		'*',
		{
			parseAs: 'buffer',
			bodyLimit: 1048576 * 200,
		},
		(req, body, done) => {
			console.log(
				`[HTTP] 收到原始请求体 类型:${req.headers['content-type']} 长度:${body.length}字节`,
			)
			done(null, body)
		},
	)
	await app.register(await import('@fastify/multipart'), {
		attachFieldsToBody: false,
		sharedSchemaId: '#multipartFormDataSchema',
	})

	await app.register(FastifyWebsocket)
	app.addHook('onRequest', (req, res, done) => {
		res.header('Access-Control-Allow-Origin', '*')
		res.header('Access-Control-Allow-Methods', '*')
		res.header('Access-Control-Allow-Headers', '*')
		if (req.method === 'OPTIONS') {
			console.log(`[HTTP] 处理OPTIONS请求 ${req.url}`)
			res.code(204).send()
		}
		done()
	})

	app.route({
		method: ['GET', 'POST', 'PUT', 'DELETE', 'PATCH'],
		url: '/proxy',
		handler: async (req, res) => {
			try {
				const { url, ...params } = req.query as Record<string, string>
				if (!url) {
					console.warn('[HTTP] 代理请求缺少URL参数')
					return res.code(400).send({ error: 'Missing URL' })
				}

				const target = new URL(url)
				Object.entries(params).forEach(([k, v]) =>
					target.searchParams.append(k, v),
				)
				console.log(`[HTTP] 开始代理请求 ${req.method} ${target.toString()}`)

				const headers: Record<string, string> = {
					'User-Agent': 'BotNodeSDK/0.0.1',
					Authorization: req.headers['authorization'] || '',
					'X-Union-Appid': Array.isArray(req.headers['x-union-appid'])
						? req.headers['x-union-appid'][0]
						: req.headers['x-union-appid'] || '',
				}

				if (req.headers['content-type']) {
					headers['Content-Type'] = req.headers['content-type']
				}

				// 确保请求体为 Buffer
				const rawBody = req.body as Buffer | null
				const bodyLength = rawBody ? rawBody.length : 0
				delete headers['transfer-encoding']

				// 正确设置 Content-Length
				if (bodyLength > 0) {
					headers['Content-Length'] = bodyLength.toString()
				} else {
					delete headers['content-length']
				}

				console.log('[HTTP] 最终请求头:', headers)

				const startTime = Date.now()
				const response = await fetch(target.toString(), {
					method: req.method,
					headers: { ...headers, 'Accept-Encoding': 'gzip, deflate, br' },
					body: Buffer.isBuffer(rawBody)
						? rawBody
						: JSON.stringify(rawBody),
				})

				console.log(
					`[HTTP] 代理响应 ${target.toString()} 状态:${response.status} 耗时:${
						Date.now() - startTime
					}ms`,
				)

				const contentEncoding = response.headers.get('Content-Encoding')
				let responseBuffer: any = Buffer.from(await response.arrayBuffer())

				if (contentEncoding) {
					console.log(`[HTTP] 检测到压缩内容 编码格式:${contentEncoding}`)
					try {
						responseBuffer = await decompressBuffer(
							responseBuffer,
							contentEncoding,
						)
						console.log(`[HTTP] 解压成功 原始长度:${responseBuffer.length}`)
					} catch (error) {
						console.error('[HTTP] 解压失败，返回原始数据', error)
					}
				}

				const responseHeaders: Record<string, string> = {}
				response.headers.forEach((value, key) => {
					const lowerKey = key.toLowerCase()
					if (
						![
							'content-length',
							'transfer-encoding',
							'content-encoding',
						].includes(lowerKey)
					) {
						responseHeaders[key] = value
					}
				})

				res.code(response.status)
				res.headers({
					...responseHeaders,
					'Content-Length': responseBuffer.length.toString(),
				})
				console.log(`[返回数据] ${response.toString()}`)
				return res.send(responseBuffer)
			} catch (error) {
				console.error('[HTTP] 代理请求异常', error)
				res.code(500).send({ error: 'Proxy error' })
			}
		},
	})

	app.get('/ws', { websocket: true }, (conn, req) => {
		const { appid, url } = req.query as QueryParams
		if (!appid || !url || !isValidUrl(url)) {
			console.warn(`[WS] 非法连接参数 appid:${appid} url:${url}`)
			return conn.close(1003)
		}

		const bot = new QQBot(conn, Number(appid), url)
		const connections = userConnections.get(appid) || []
		userConnections.set(appid, [...connections, bot])
		console.log(`[WS] 当前连接数 appid:${appid} 数量:${connections.length + 1}`)
	})

	app.get('/health', (_, res) => {
		console.debug('[HTTP] 健康检查请求')
		res.send({ status: 'ok', connections: userConnections.size })
	})

	const port = parseInt(process.env.PORT || '3000', 10)
	app.listen({ port, host: '0.0.0.0' }, (err, addr) => {
		if (err) {
			console.error('[SERVER] 服务器启动失败', err)
			process.exit(1)
		}
		console.log(`[SERVER] 服务器已启动 监听地址:${addr}`)
	})
}

function isValidUrl(url: string) {
	try {
		new URL(url)
		return true
	} catch {
		console.warn(`[WS] 非法URL格式 ${url}`)
		return false
	}
}

startServer()
