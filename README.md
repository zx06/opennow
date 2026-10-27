# opennow

自托管的 [here.now](https://here.now) 风格静态站点发布器：单个 Go 二进制（`CGO_ENABLED=0`），支持 CLI 与内嵌中文管理后台。

## 功能（MVP）

- **发布流程**：创建修订 → 上传文件 → _finalize_ 上线
- **路由**：子域名 `{slug}.<BASE_DOMAIN>` 与路径 `/<slug>/` 可同时启用
- **访问控制**：站点密码（HMAC 签名 Cookie，按站点路径 scoped）、到期时间、版本回滚、删除
- **历史版本预览**：任意历史版本可通过 `/_preview/<slug>/<versionId>/` 独立预览，不影响线上版本
- **存储**：本地磁盘（默认）、S3 兼容对象存储（AWS S3 / MinIO 等）、Cloudflare R2、GitHub 仓库；见「[存储后端](#存储后端)」
- **管理**：`/_admin/` 内嵌 Alpine.js（无 CDN、无前端构建）
- **登录**：GitHub / Google / Keycloak / Authentik 等 OAuth2 / OIDC 登录（PKCE、白名单），API 密钥登录，可选 Cloudflare Access；见「[登录与身份认证](#登录与身份认证oauth2--oidc)」

## 环境要求

- **Go 1.26 或更高版本**（`go.mod` 声明 `go 1.26.0`；CI 与 Dockerfile 均使用 1.26）
- 无需 CGO，无需 Node/前端构建工具

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

环境变量：`OPENNOW_API_KEY`（必需，CLI 以 `Authorization: Bearer` 发送）、`OPENNOW_SERVER`（CLI 默认 `http://127.0.0.1:8080`）。

## 路径模式与资源链接

在路径模式 `https://host/<slug>/` 下，HTML 中的 **根绝对路径**（如 `href="/style.css"`）会被浏览器请求为 `https://host/style.css`。opennow 会根据 **Referer** 尝试解析到对应站点的 `style.css`。

**推荐**在站点内使用相对路径（`style.css`、`./assets/app.js`），可跨子域名/路径模式且更可靠。

## 环境变量

| 变量 | 默认 | 说明 |
|------|------|------|
| `OPENNOW_ADDR` | `:8080` | 监听地址 |
| `OPENNOW_DATA_DIR` | `./data` | 本地存储目录 |
| `OPENNOW_STORAGE` | `local` | 存储后端：`local` / `s3` / `r2` / `github`，各自配置见「存储后端」 |
| `OPENNOW_API_KEY` | — | 管理 API 密钥 |
| `OPENNOW_BASE_DOMAIN` | — | 子域名后缀，如 `xuzhuo.xyz` |
| `OPENNOW_PUBLIC_URL` | — | 路径模式公开 URL 前缀 |
| `OPENNOW_ADMIN_PREFIX` | `/_admin/` | 管理 UI 路径 |
| `OPENNOW_ADMIN_HOST` | — | 独立管理域名，如 `admin.xuzhuo.xyz` |
| `OPENNOW_PATH_MODE` | `true` | 路径路由 |
| `OPENNOW_SUBDOMAIN_MODE` | `true` | 子域名路由 |
| `OPENNOW_SESSION_TTL` | `168h` | 后台会话有效期 |
| `OPENNOW_ADMIN_API_KEY_LOGIN` | `true` | 是否允许在后台用 API 密钥登录（CLI Bearer 不受影响） |
| `OPENNOW_OAUTH_*` / `OPENNOW_CF_ACCESS_*` | — | OAuth2 / OIDC 与 Cloudflare Access，见「登录与身份认证」 |
| `OPENNOW_SECRET` | 自动生成 | 站点解锁 Cookie、后台会话与预览令牌的 HMAC 签名密钥；未设置时首次启动生成并保存到 `<DATA_DIR>/secret.key`（0600）。多实例部署需设置为同一值 |

## 登录与身份认证（OAuth2 / OIDC）

管理后台支持三类凭据，可同时启用：

| 方式 | 适用 | 说明 |
|------|------|------|
| **OAuth2 / OIDC 登录** | 浏览器 | 授权码流程 + PKCE（S256）+ `state`；OIDC 另校验 `nonce` 与 ID Token（JWKS 签名、`iss`、`aud`、`exp`）。支持多个提供方 |
| **API 密钥登录** | 浏览器 | 登录卡片中输入 `OPENNOW_API_KEY`；可用 `OPENNOW_ADMIN_API_KEY_LOGIN=false` 关闭 |
| **Bearer API 密钥** | CLI / 脚本 | `Authorization: Bearer $OPENNOW_API_KEY`，不受上一项影响，CLI 默认使用此方式 |
| **Cloudflare Access**（可选） | 浏览器 | 校验 `Cf-Access-Jwt-Assertion`（团队 JWKS 签名、`aud`、`iss`、`exp`），与 `Cf-Access-Authenticated-User-Email` 比对 |

登录成功后服务端签发 **HMAC 签名会话 Cookie**（`opennow_session`，复用 `OPENNOW_SECRET` / `<DATA_DIR>/secret.key`），`HttpOnly`、`SameSite=Lax`，`OPENNOW_PUBLIC_URL` 为 `https://` 时自动加 `Secure`；有效期 `OPENNOW_SESSION_TTL`（默认 `168h`）。退出：`POST /_auth/logout`（或 `POST /api/v1/auth/logout`）。

### 回调地址

在提供方登记的回调（Redirect URI）为：

```
<OPENNOW_PUBLIC_URL>/_auth/callback/<provider>
```

例如 `https://now.example.com/_auth/callback/google`（通用）；名为 `github` 的提供方为兼容既有 OAuth App，回调固定为 `https://now.example.com/api/v1/auth/github/callback`。启用 OAuth 时 **必须** 设置 `OPENNOW_PUBLIC_URL`。会话 Cookie 写在回调所在域名上；若后台使用独立域名（`OPENNOW_ADMIN_HOST`），请设置 `OPENNOW_OAUTH_BASE_URL=https://admin.example.com`，回调与 Cookie 将使用该地址。

### 提供方配置

`OPENNOW_OAUTH_PROVIDERS` 为逗号分隔的提供方名称（小写字母、数字、`-`、`_`），每个提供方用 `OPENNOW_OAUTH_<NAME>_*` 配置（`<NAME>` 为大写、`-` 换成 `_`）：

| 变量 | 说明 |
|------|------|
| `_TYPE` | `github` / `oidc` / `oauth2`（名称为 `github` 或 `oidc` 时可省略） |
| `_CLIENT_ID` / `_CLIENT_SECRET` | 客户端凭据 |
| `_ISSUER` | OIDC Issuer，自动读取 `<issuer>/.well-known/openid-configuration` |
| `_AUTH_URL` / `_TOKEN_URL` / `_USERINFO_URL` | 通用 OAuth2 端点（`oauth2` 必填；`github`/`oidc` 可用于覆盖） |
| `_API_URL` | GitHub API 地址（GitHub Enterprise：`https://ghe.example.com/api/v3`） |
| `_SCOPES` | 空格或逗号分隔；默认 OIDC `openid profile email`，GitHub `read:user user:email`（配置组织白名单时追加 `read:org`） |
| `_DISPLAY_NAME` | 按钮文字，显示为「使用 <DISPLAY_NAME> 登录」 |
| `_USER_FIELD` / `_EMAIL_FIELD` / `_NAME_FIELD` / `_AVATAR_FIELD` | 声明映射，支持点号嵌套（如 `data.login`）。默认用户名依次取 `preferred_username`、`login`、`username`、`nickname`、`sub` |
| `_TRUST_EMAIL` | 提供方不返回 `email_verified` 时，是否视邮箱为已验证（默认 `false`；Entra ID 等可设 `true`） |

也可用 `OPENNOW_OAUTH_CONFIG=/path/auth.json` 提供 JSON 配置文件（字段为上表的 snake_case，如 `client_id`、`userinfo_url`），环境变量会追加列表/覆盖标量：

```json
{
  "providers": [
    {"name": "google", "type": "oidc", "client_id": "…", "client_secret": "…",
     "issuer": "https://accounts.google.com", "display_name": "Google"}
  ],
  "allowed_emails": ["me@gmail.com"],
  "session_ttl": "24h",
  "api_key_login": false
}
```

### 白名单（默认拒绝）

| 变量 | 示例 | 说明 |
|------|------|------|
| `OPENNOW_OAUTH_ALLOWED_USERS` | `github:zx06,keycloak:alice` | `<提供方>:<用户名或 sub>`，不区分大小写，按提供方隔离 |
| `OPENNOW_OAUTH_ALLOWED_EMAILS` | `me@example.com` | 仅匹配**已验证**邮箱 |
| `OPENNOW_OAUTH_ALLOWED_DOMAINS` | `example.com` | 已验证邮箱的域名（精确匹配，不含子域） |
| `OPENNOW_OAUTH_ALLOWED_GITHUB_ORGS` | `my-org` | GitHub 组织成员（`/user/memberships/orgs/<org>` 状态为 `active`） |
| `OPENNOW_OAUTH_ALLOW_ANY` | `false` | 设为 `true` 时允许任意通过提供方认证的账号（慎用） |

白名单全部为空且未设置 `ALLOW_ANY` 时，所有 OAuth 登录都会被拒绝（启动日志会提示）。白名单在每次请求时重新校验，移除用户后其会话立即失效。

### 示例：GitHub

在 GitHub → Settings → Developer settings → **OAuth Apps** 新建应用，Authorization callback URL 填 `https://now.example.com/api/v1/auth/github/callback`（名为 `github` 的提供方固定使用该路径，避免已有 OAuth App 改 URL）。登录只认统一的 `OPENNOW_OAUTH_*`：

```bash
OPENNOW_PUBLIC_URL=https://now.example.com
OPENNOW_OAUTH_PROVIDERS=github
OPENNOW_OAUTH_GITHUB_CLIENT_ID=Iv1.xxxxxxxx
OPENNOW_OAUTH_GITHUB_CLIENT_SECRET=xxxxxxxx
OPENNOW_OAUTH_ALLOWED_USERS=github:zx06
# 可选：拒绝提示 / 组织成员均可登录（需组织允许该 OAuth App 访问）
# OPENNOW_OAUTH_DENY_MESSAGE=仅内部账号
# OPENNOW_OAUTH_ALLOWED_GITHUB_ORGS=my-org
```

**迁移**（旧登录变量已移除，不再映射）：

| 旧（已失效） | 新 |
|--------------|----|
| `OPENNOW_GITHUB_CLIENT_ID` | `OPENNOW_OAUTH_GITHUB_CLIENT_ID`（并设 `OPENNOW_OAUTH_PROVIDERS=github`） |
| `OPENNOW_GITHUB_CLIENT_SECRET` | `OPENNOW_OAUTH_GITHUB_CLIENT_SECRET` |
| `OPENNOW_GITHUB_ALLOWLIST=zx06` | `OPENNOW_OAUTH_ALLOWED_USERS=github:zx06` |
| `OPENNOW_GITHUB_DENY_MESSAGE` | `OPENNOW_OAUTH_DENY_MESSAGE` |

存储后端的 `OPENNOW_GITHUB_TOKEN` / `OPENNOW_GITHUB_REPO` / `OPENNOW_GITHUB_BRANCH` 等**不受影响**（那是 GitHub storage，不是登录）。

GitHub 不是 OIDC 提供方，opennow 使用 `/user` 与 `/user/emails`（仅取已验证的主邮箱）获取身份。

### 示例：Google

Google Cloud Console → API 和服务 → 凭据 → 创建 OAuth 客户端 ID（Web 应用），已获授权的重定向 URI 填 `https://now.example.com/_auth/callback/google`。

```bash
OPENNOW_OAUTH_PROVIDERS=google
OPENNOW_OAUTH_GOOGLE_TYPE=oidc
OPENNOW_OAUTH_GOOGLE_ISSUER=https://accounts.google.com
OPENNOW_OAUTH_GOOGLE_CLIENT_ID=xxxx.apps.googleusercontent.com
OPENNOW_OAUTH_GOOGLE_CLIENT_SECRET=GOCSPX-xxxx
OPENNOW_OAUTH_GOOGLE_DISPLAY_NAME=Google
OPENNOW_OAUTH_ALLOWED_EMAILS=me@gmail.com
# 或 Google Workspace 整个域：OPENNOW_OAUTH_ALLOWED_DOMAINS=example.com
```

### 示例：Keycloak / Authentik（OIDC）

```bash
OPENNOW_OAUTH_PROVIDERS=keycloak
OPENNOW_OAUTH_KEYCLOAK_TYPE=oidc
OPENNOW_OAUTH_KEYCLOAK_ISSUER=https://sso.example.com/realms/main
OPENNOW_OAUTH_KEYCLOAK_CLIENT_ID=opennow
OPENNOW_OAUTH_KEYCLOAK_CLIENT_SECRET=xxxx
OPENNOW_OAUTH_KEYCLOAK_DISPLAY_NAME=公司 SSO
OPENNOW_OAUTH_ALLOWED_USERS=keycloak:alice
```

- **Keycloak**：Clients → Create（OpenID Connect，Client authentication 开启），Valid redirect URIs 填 `https://now.example.com/_auth/callback/keycloak`；Issuer 为 `https://<host>/realms/<realm>`。
- **Authentik**：Applications → Providers → 新建 OAuth2/OpenID Provider（Confidential），Redirect URIs 填回调地址；Issuer 为 `https://<host>/application/o/<应用 slug>/`（注意末尾斜杠需与 discovery 中的 `issuer` 完全一致）。名称可用 `authentik`，变量前缀即 `OPENNOW_OAUTH_AUTHENTIK_*`。

多个提供方可并存：`OPENNOW_OAUTH_PROVIDERS=github,keycloak`，登录卡片会显示多个按钮。

### 示例：通用 OAuth2（如 Gitea）

```bash
OPENNOW_OAUTH_PROVIDERS=gitea
OPENNOW_OAUTH_GITEA_TYPE=oauth2
OPENNOW_OAUTH_GITEA_AUTH_URL=https://git.example.com/login/oauth/authorize
OPENNOW_OAUTH_GITEA_TOKEN_URL=https://git.example.com/login/oauth/access_token
OPENNOW_OAUTH_GITEA_USERINFO_URL=https://git.example.com/api/v1/user
OPENNOW_OAUTH_GITEA_CLIENT_ID=…
OPENNOW_OAUTH_GITEA_CLIENT_SECRET=…
OPENNOW_OAUTH_GITEA_USER_FIELD=login
OPENNOW_OAUTH_ALLOWED_USERS=gitea:zx06
```

### 示例：Cloudflare Access

在 Cloudflare Zero Trust 中为 opennow 管理域名（建议配合 `OPENNOW_ADMIN_HOST`）创建 Self-hosted 应用，在应用概览中复制 **Application Audience (AUD) Tag**：

```bash
OPENNOW_CF_ACCESS_TEAM_DOMAIN=myteam            # 或 myteam.cloudflareaccess.com
OPENNOW_CF_ACCESS_AUD=4714c1358e65fe4b408ad6d432a5f878f08194bdb4752441fd56faefa9b2b6f2
# 二选一：
OPENNOW_OAUTH_ALLOWED_DOMAINS=example.com       # 仍按 opennow 白名单（邮箱/域名，用户为 cloudflare:<email>）
# OPENNOW_CF_ACCESS_ALLOW_ANY=true              # 完全信任 Access 策略
```

经 Access 认证的请求会自动登录后台（每个请求都校验 JWT，密钥按需从 `https://<team>.cloudflareaccess.com/cdn-cgi/access/certs` 获取并缓存）。仅有邮箱请求头而无有效 JWT 的请求会被忽略，因此绕过 Cloudflare 直连源站也无法伪造身份。

### CSRF 与重定向

- 依赖 Cookie / Cloudflare Access 的写操作（POST/PUT/PATCH/DELETE）必须携带请求头 `X-OpenNow-CSRF`（管理后台自动添加），且浏览器发送的 `Origin` / `Sec-Fetch-Site` 必须为同源；否则返回 403。Bearer 请求不受影响
- 登录后的跳转地址（`/_auth/login/<provider>?redirect=`）只接受站内相对路径，拒绝 `//host`、`/\host`、绝对 URL 与控制字符
- `state`、`nonce`、PKCE `code_verifier` 存放在 10 分钟有效、仅作用于 `/_auth/` 的签名 Cookie 中，回调时一次性使用

> **兼容性提示**：旧版 CLI 通过 `POST /api/v1/auth/login` 换取 Cookie 后执行写操作，会因缺少 CSRF 请求头被拒绝；请升级 CLI（新版使用 Bearer）。

## 存储后端

通过 `OPENNOW_STORAGE` 选择。所有后端实现同一接口（创建修订 → 上传 → finalize、元数据、版本列表/回滚、流式读取、Content-Type），管理后台首页「存储详情」会显示当前后端类型、位置、缓存命中与请求计数等信息。

远程后端共用同一对象布局（可加前缀）：

```
sites/<slug>/meta.json                      站点元数据（当前版本指针 = 发布的提交点）
sites/<slug>/versions.json                  版本索引（列出版本无需 LIST）
sites/<slug>/versions/<vid>/manifest.json   版本文件清单（大小、Content-Type、git SHA）
sites/<slug>/versions/<vid>/files/<path>    文件内容
```

版本不可变，因此清单和文件可以长期缓存；读取不存在的文件由清单直接判定，不产生远程请求。

> 使用远程后端时 `OPENNOW_DATA_DIR` 仍用于保存 `secret.key`（GitHub 后端还用于暂存上传中的文件）。多实例部署请设置相同的 `OPENNOW_SECRET`。

### 缓存（S3 / R2 / GitHub 通用）

| 变量 | 默认（S3 / R2 / GitHub） | 说明 |
|------|------|------|
| `OPENNOW_CACHE_META_TTL` | `30s` / `5m` / `30s` | 元数据（meta、版本索引、站点列表；GitHub 为分支 HEAD）的缓存时间。本进程的写入会立即更新缓存；该值只决定**其它实例**写入后多久可见。`0` 关闭 |
| `OPENNOW_CACHE_FILE_MB` | `64` / `128` / `128` | 热文件内存缓存总量 (MiB) |
| `OPENNOW_CACHE_FILE_MAX_KB` | `1024` / `2048` / — | 单个文件进入缓存的上限 (KiB)，更大的文件直接流式转发 |
| `OPENNOW_CACHE_FILE_TTL` | `1h` / `6h` / — | 缓存文件过期时间（内容不可变，仅用于释放内存） |

### S3（AWS S3、MinIO 及其它 S3 兼容服务）

基于 [minio-go](https://github.com/minio/minio-go)（纯 Go，无 CGO）。

| 变量 | 默认 | 说明 |
|------|------|------|
| `OPENNOW_S3_ENDPOINT` | `s3.amazonaws.com`（https） | 例如 `https://s3.us-west-2.amazonaws.com`、`http://127.0.0.1:9000`；不带协议时默认 https |
| `OPENNOW_S3_REGION` | `us-east-1`（或 `AWS_REGION`） | 区域 |
| `OPENNOW_S3_BUCKET` | — | **必填** |
| `OPENNOW_S3_PREFIX` | — | 对象键前缀，如 `opennow/` |
| `OPENNOW_S3_ACCESS_KEY_ID` | `AWS_ACCESS_KEY_ID` | 访问密钥 |
| `OPENNOW_S3_SECRET_ACCESS_KEY` | `AWS_SECRET_ACCESS_KEY` | 私有密钥 |
| `OPENNOW_S3_PATH_STYLE` | 自动 | `true` 强制 path-style（MinIO 推荐），`false` 强制虚拟主机风格 |
| `OPENNOW_S3_INSECURE` | `false` | endpoint 不带协议时使用 http |
| `OPENNOW_S3_CREATE_BUCKET` | `false` | 启动时自动创建 bucket |

```bash
OPENNOW_STORAGE=s3 \
OPENNOW_S3_ENDPOINT=http://127.0.0.1:9000 OPENNOW_S3_PATH_STYLE=true \
OPENNOW_S3_BUCKET=opennow OPENNOW_S3_CREATE_BUCKET=true \
OPENNOW_S3_ACCESS_KEY_ID=minioadmin OPENNOW_S3_SECRET_ACCESS_KEY=minioadmin \
./opennow serve
```

请求开销：每次发布 = 每个文件 1 次 PUT + 4 次（草稿、清单、版本索引、meta）+ 1 次批量 DELETE；访问站点在缓存命中时为 0 次请求，冷启动最多 3 次 GET（meta、清单、文件）。只有后台站点列表与删除站点会使用 LIST。

### Cloudflare R2

R2 使用 S3 兼容 API，自动套用 R2 默认值：endpoint `https://<account_id>.r2.cloudflarestorage.com`、region `auto`、path-style，并使用更长的缓存（元数据 5 分钟、热文件 128 MiB），以节省免费额度中的 Class A/B 操作次数。

| 变量 | 说明 |
|------|------|
| `OPENNOW_R2_ACCOUNT_ID` | **必填**（或设置 `OPENNOW_R2_ENDPOINT`） |
| `OPENNOW_R2_BUCKET` | **必填** |
| `OPENNOW_R2_ACCESS_KEY_ID` / `OPENNOW_R2_SECRET_ACCESS_KEY` | R2 API 令牌（「管理 R2 API 令牌」中创建，需对该 bucket 的对象读写权限） |
| `OPENNOW_R2_PREFIX` | 可选对象键前缀 |
| `OPENNOW_R2_ENDPOINT` | 可选，覆盖 endpoint（如欧盟辖区 `https://<account_id>.eu.r2.cloudflarestorage.com`） |
| `OPENNOW_R2_CREATE_BUCKET` | `true` 时启动自动创建 bucket（令牌需管理权限，通常不需要） |

```bash
OPENNOW_STORAGE=r2 OPENNOW_R2_ACCOUNT_ID=<account_id> OPENNOW_R2_BUCKET=opennow \
OPENNOW_R2_ACCESS_KEY_ID=... OPENNOW_R2_SECRET_ACCESS_KEY=... ./opennow serve
```

提示：后台「存储详情」显示本进程的 PUT/GET/LIST/DELETE 计数，可用于估算额度消耗。R2 后端通过 MinIO 集成测试覆盖同一代码路径，尚未对真实 R2 账号做端到端测试。

### GitHub 仓库

站点以文件形式保存在 GitHub 仓库中，使用 REST git data API：每次发布 = **一个 commit**（blob → tree → commit → 快进更新分支），多文件版本原子生效；回滚、修改设置、删除站点也各是一个 commit。分支被其它写入方抢先更新时会自动基于新 HEAD 重试。

| 变量 | 默认 | 说明 |
|------|------|------|
| `OPENNOW_GITHUB_TOKEN` | `GITHUB_TOKEN` | **必填**。fine-grained token 需该仓库 *Contents: Read and write*；classic token 需 `repo` |
| `OPENNOW_GITHUB_REPO` | — | **必填**，`owner/repo` |
| `OPENNOW_GITHUB_BRANCH` | `main` | 分支（不存在时自动创建；空仓库也可） |
| `OPENNOW_GITHUB_PREFIX` | — | 仓库内路径前缀，如 `opennow` |
| `OPENNOW_GITHUB_API_URL` | `https://api.github.com` | GitHub Enterprise Server 填 `https://<host>/api/v3` |
| `OPENNOW_GITHUB_MAX_FILE_MB` | `100` | 单文件上限（不能超过 100） |
| `OPENNOW_GITHUB_WRITE_INTERVAL` | `250ms` | 创建 blob 请求之间的最小间隔（避免触发二级限流） |

```bash
OPENNOW_STORAGE=github OPENNOW_GITHUB_REPO=me/my-sites OPENNOW_GITHUB_TOKEN=github_pat_... \
OPENNOW_GITHUB_PREFIX=opennow ./opennow serve
```

请求与限流：

- 上传先暂存到 `<DATA_DIR>/github-drafts/`，finalize 时统一提交；≤512 KiB 的 UTF-8 文本文件直接内联进 tree 请求，与上一版本相同的文件（按 git SHA 判断）直接复用，不重复上传，只有新的二进制文件需要单独创建 blob
- 读取通过 tree/blob（按 SHA 不可变，内存缓存）；分支 HEAD 每 `OPENNOW_CACHE_META_TTL` 用带 ETag 的条件请求检查一次（304 不计入配额）。缓存命中时访问站点不产生 API 请求
- 跟踪 `X-RateLimit-*` 响应头；配额耗尽或收到 `Retry-After` 时在窗口内直接失败而不再请求 GitHub（≤60 秒的二级限流会等待后重试一次），读取在 GitHub 不可用时继续使用最后已知的 HEAD。剩余配额显示在后台「存储详情」

限制：

- 单文件最大 100 MiB（GitHub API 限制），超出返回 413；仓库建议控制在 1–5 GiB 内
- 所有历史都保留在 git 中：删除站点只是新增一个删除 commit，**不会**缩小仓库，也不会从历史中移除内容
- `meta.json` 中保存站点密码的哈希；请使用**私有仓库**。公开仓库会让任何人看到全部站点内容（包括设置了密码的站点）和密码哈希
- 每次发布都是一次 commit，频繁发布会使提交历史快速增长；大型站点（数百个二进制文件）首次发布会因 blob 创建限流而较慢
- 适合个人/小规模使用；不建议多个 opennow 实例高频并发写入同一分支（虽然有冲突重试）

### 测试

```bash
CGO_ENABLED=0 go test ./...      # 单元测试（S3 使用内存实现，GitHub 使用内存 git 仓库）

# S3/R2 集成测试（例如本地 MinIO）
OPENNOW_TEST_S3_ENDPOINT=http://127.0.0.1:9000 \
OPENNOW_TEST_S3_ACCESS_KEY=minioadmin OPENNOW_TEST_S3_SECRET_KEY=minioadmin \
go test ./internal/storage/s3/ -run Integration -v

# GitHub 端到端测试（使用一次性私有仓库；每次运行使用独立前缀并在结束时删除站点）
OPENNOW_TEST_GITHUB_REPO=me/opennow-storage-test OPENNOW_TEST_GITHUB_TOKEN=$(gh auth token) \
go test ./internal/storage/github/ -run E2E -v
```

CI 中 MinIO 集成测试以服务容器运行；GitHub 端到端测试仅在仓库配置了 `OPENNOW_TEST_GITHUB_REPO` 变量和 `OPENNOW_TEST_GITHUB_TOKEN` secret 时运行。

## 历史版本预览

管理后台「管理 → 版本历史」中每个版本都有「预览」按钮（新标签页打开），以及文件列表、大小、时间与「当前线上」标记；「设为当前」即回滚。

- 预览地址：`/_preview/<slug>/<versionId>/`，直接读取该版本文件，**不会修改当前版本**
- 访问控制：需管理员登录会话，或携带签名令牌 `?token=…`（`GET /api/v1/sites/{slug}/versions` 返回的 `preview_url`，24 小时有效，仅对该版本有效）。令牌首次校验后写入仅作用于该版本预览路径的 Cookie，便于加载相对路径资源
- 相关 API：`GET /api/v1/sites/{slug}/versions`（含 `current`、`file_count`、`total_bytes`、`preview_url`）、`GET /api/v1/sites/{slug}/versions/{versionId}/files`

## 安全说明

- 站点解锁 Cookie 值为 `过期时间.HMAC-SHA256(secret, slug, 密码哈希, 过期时间)`，服务端校验；修改或清除密码会使旧 Cookie 失效
- 解锁 Cookie 路径：路径模式为 `/<slug>/`，子域名模式为 `/`
- 站点 API 响应不再返回 `password_hash`，改为 `has_password`
- 密码页返回 HTTP 401
- 后台会话为无状态 HMAC 签名 Cookie：更换 `OPENNOW_SECRET` 会使所有会话失效；API 密钥会话绑定当前密钥，更换 `OPENNOW_API_KEY` 即失效
- 路径模式下用户站点与管理后台同源，站点内脚本可携带管理员 Cookie 访问同源 API；生产环境建议用 `OPENNOW_ADMIN_HOST` 将后台放在独立域名，或使用子域名模式发布站点

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
