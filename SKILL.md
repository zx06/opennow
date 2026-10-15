# opennow — Agent 发布指南

当用户要在 **自托管 opennow** 上发布静态文件/站点时使用本技能（非 here.now 云服务）。

## 前置条件

- 运行中的 opennow 实例（`opennow serve` 或 Docker）
- 环境变量 **`OPENNOW_API_KEY`**（与服务器配置一致）
- 可选 **`OPENNOW_SERVER`**（默认 `http://127.0.0.1:8080`）

## 发布站点

```bash
opennow publish [--slug SLUG] [--server URL] <文件或目录>
```

- 目录内容成为站点根；应有 `index.html` 在目录根部。
- 省略 `--slug` 时自动生成 slug。
- 输出 JSON 含 `slug` 与 `urls`（路径/子域名 URL）。

示例：

```bash
export OPENNOW_API_KEY=your-secret-key
opennow publish --slug demo ./my-site
```

## 其它 CLI（与后台 API 一致）

```bash
opennow list
opennow delete <slug>
opennow rollback <slug> <version-id>
```

## HTTP API（与 CLI 同权）

登录（会话 Cookie）：

```http
POST /api/v1/auth/login
{"api_key":"..."}
```

发布三步：

1. `POST /api/v1/publish` → `{revision_id, slug}`
2. `PUT /api/v1/publish/{revisionId}/files/{path}` → 文件 body
3. `POST /api/v1/publish/{revisionId}/finalize`

管理：`GET/PATCH/DELETE /api/v1/sites/{slug}`，`GET .../versions`，`POST .../rollback`。

## 内容建议

- 资源链接优先 **相对路径**（`./style.css`），路径模式下根绝对路径依赖 Referer 解析。
- 密码站点：访问者 `POST /api/v1/sites/{slug}/unlock` 或 HTML 表单解锁。

## 管理 UI

浏览器打开 `{PUBLIC_URL}/_admin/`，使用 API 密钥登录；支持拖放发布、版本回滚、密码/到期设置。
