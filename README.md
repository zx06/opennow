# opennow

自托管的 [here.now](https://here.now) 风格静态站点发布器：单个 Go 二进制（`CGO_ENABLED=0`），支持 CLI 与内嵌中文管理后台。

## 功能（MVP）

- **发布流程**：创建修订 → 上传文件 → _finalize_ 上线
- **路由**：子域名 `{slug}.<BASE_DOMAIN>` 与路径 `/<slug>/` 可同时启用
- **访问控制**：站点密码（按站点路径 scoped cookie）、到期时间、版本回滚、删除
- **存储**：本地磁盘（默认可用）；S3 / R2 / GitHub 后端接口已预留
- **管理**：`/_admin/` 内嵌 Alpine.js（无 CDN、无前端构建）

## 快速开始（本地存储）

```bash
# 构建
CGO_ENABLED=0 go build -o opennow ./cmd/opennow

# 启动（需设置管理密钥）
export OPENNOW_API_KEY=your-secret-key
export OPENNOW_DATA_DIR=./data
export OPENNOW_PUBLIC_URL=http://127.0.0.1:8080   # 可选，用于 CLI/后台展示路径 URL
# export OPENNOW_BASE_DOMAIN=xuzhuo.xyz         # 子域名模式需通配 DNS

./opennow serve
```

准备站点目录（**请使用相对资源路径**；见下文「路径模式与资源链接」）：

```bash
mkdir -p mysite
echo '<!doctype html><html><body>Hello</body></html>' > mysite/index.html
```

发布：

```bash
export OPENNOW_API_KEY=your-secret-key
./opennow publish --slug mysite ./mysite
```

浏览器访问：`http://127.0.0.1:8080/mysite/`  
管理后台：`http://127.0.0.1:8080/_admin/`

## CLI

| 命令 | 说明 |
|------|------|
| `opennow serve` | 启动 HTTP 服务 |
| `opennow publish [--slug SLUG] [--server URL] <path>` | 发布文件或目录 |
| `opennow list [--server URL]` | 列出站点 |
| `opennow delete <slug>` | 删除站点 |
| `opennow rollback <slug> <version-id>` | 回滚版本 |

环境变量：`OPENNOW_API_KEY`（必需，CLI/后台）、`OPENNOW_SERVER`（CLI 默认 `http://127.0.0.1:8080`）。

## 路径模式与资源链接

在路径模式 `https://host/<slug>/` 下，HTML 中的 **根绝对路径**（如 `href="/style.css"`）会被浏览器请求为 `https://host/style.css`。opennow 会根据 **Referer** 尝试解析到对应站点的 `style.css`。

**推荐**在站点内使用相对路径（`style.css`、`./assets/app.js`），可跨子域名/路径模式且更可靠。

## 环境变量

| 变量 | 默认 | 说明 |
|------|------|------|
| `OPENNOW_ADDR` | `:8080` | 监听地址 |
| `OPENNOW_DATA_DIR` | `./data` | 本地存储目录 |
| `OPENNOW_STORAGE` | `local` | 存储后端 |
| `OPENNOW_API_KEY` | — | 管理 API 密钥 |
| `OPENNOW_BASE_DOMAIN` | — | 子域名后缀，如 `xuzhuo.xyz` |
| `OPENNOW_PUBLIC_URL` | — | 路径模式公开 URL 前缀 |
| `OPENNOW_ADMIN_PREFIX` | `/_admin/` | 管理 UI 路径 |
| `OPENNOW_ADMIN_HOST` | — | 独立管理域名，如 `admin.xuzhuo.xyz` |
| `OPENNOW_PATH_MODE` | `true` | 路径路由 |
| `OPENNOW_SUBDOMAIN_MODE` | `true` | 子域名路由 |

## Docker

```bash
docker build -t opennow .
docker run --rm -p 8080:8080 \
  -e OPENNOW_API_KEY=your-secret-key \
  -v opennow-data:/data \
  opennow
```

## 许可

MIT — 见 [LICENSE](LICENSE)。
