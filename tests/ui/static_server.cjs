const http = require('node:http')
const fs = require('node:fs')
const path = require('node:path')

const rootDir = path.resolve(__dirname, '..', '..')
const webDir = path.join(rootDir, 'web')
const staticDir = path.join(webDir, 'static')
const indexPath = path.join(webDir, 'templates', 'index.html')

const port = Number(process.env.UI_TEST_PORT || 4173)

const contentTypes = {
    '.html': 'text/html; charset=utf-8',
    '.css': 'text/css; charset=utf-8',
    '.js': 'application/javascript; charset=utf-8',
    '.svg': 'image/svg+xml'
}

function sendFile(res, filePath) {
    fs.readFile(filePath, (err, data) => {
        if (err) {
            res.writeHead(404)
            res.end('Not found')
            return
        }
        const ext = path.extname(filePath)
        res.writeHead(200, { 'Content-Type': contentTypes[ext] || 'application/octet-stream' })
        res.end(data)
    })
}

const server = http.createServer((req, res) => {
    const url = new URL(req.url, 'http://127.0.0.1')
    if (url.pathname.startsWith('/static/')) {
        const relativePath = url.pathname.replace('/static/', '')
        const filePath = path.join(staticDir, relativePath)
        return sendFile(res, filePath)
    }
    return sendFile(res, indexPath)
})

server.listen(port, '127.0.0.1', () => {
    console.log(`UI test server listening on http://127.0.0.1:${port}`)
})
