import fs from 'fs'
import path from 'path'
import { createRequire } from 'module'

const require = createRequire(import.meta.url)
const pluginsDir = './plugins'
const yourUrl = 'example.com'
// 使用find方法快速定位插件
const findPlugin = () =>
	fs.readdirSync(pluginsDir).find((p) => {
		const pkgPath = path.join(pluginsDir, p, 'package.json')
		try {
			const { dependencies } = JSON.parse(fs.readFileSync(pkgPath, 'utf-8'))
			return dependencies?.['qq-official-bot']
		} catch {
			return false
		}
	})

const plugin = findPlugin()
if (!plugin) throw new Error('请先安装XiaoyeQQBot插件')

try {
	const { SessionManager } = require(path.join(
		process.cwd(),
		pluginsDir,
		plugin,
		'node_modules/qq-official-bot/lib/sessionManager.js',
	))

	Object.assign(SessionManager.prototype, {
		getWsUrl: async function () {
			const { sandbox, appid } = this.bot.config
			const base = sandbox
				? `https://${yourUrl}/proxy?url=https://sandbox.api.sgroup.qq.com`
				: `https://${yourUrl}/proxy?url=https://api.sgroup.qq.com`

			this.bot.request.defaults.baseURL = base
			return new Promise((resolve) => {
				this.bot.request
					.get('/gateway/bot', {
						headers: {
							Accept: '*/*',
							'Accept-Encoding': 'utf-8',
							'Accept-Language': 'zh-CN,zh;q=0.8',
							Connection: 'keep-alive',
							'User-Agent': 'v1',
							Authorization: '',
						},
					})
					.then((res) => {
						if (!res.data) throw new Error('获取ws连接信息异常')
						this.wsUrl = `wss://${yourUrl}/ws?url=${res.data.url}&appid=${appid}`
						logger.info(`WebSocket URL 已更新: ${this.wsUrl}`)
						resolve(this.wsUrl)
					})
			})
		},
	})
} catch (error) {
	console.error('插件加载失败:', error)
	process.exit(1)
}
