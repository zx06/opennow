// opennow admin — Alpine.js component (vendored, no build step).
function adminApp() {
  const api = (p, opt) => fetch('/api/v1' + p, Object.assign({ credentials: 'same-origin' }, opt));
  const json = (body, method = 'POST') => ({ method, headers: { 'Content-Type': 'application/json' }, body: JSON.stringify(body) });
  const encPath = (p) => p.split('/').map(encodeURIComponent).join('/');

  const LOGIN_ERRORS = {
    invalid_state: '登录状态已失效或被篡改，请重新登录',
    exchange_failed: '向 GitHub 验证登录失败（常见原因：出网超时、代理未配置、Client Secret 错误）。请查看服务端日志后重试',
    not_allowed: '', // filled dynamically
    denied: '已在 GitHub 取消授权',
    provider_error: 'GitHub 返回错误，请重试',
    not_configured: '服务端未配置 GitHub OAuth',
    session_failed: '创建会话失败，请重试',
  };

  return {
    ready: false, authed: false, busy: false, apiKey: '', query: '',
    githubEnabled: false, apiKeyLogin: true, githubLoginURL: '/api/v1/auth/github/login',
    denyMessage: '', allowlist: [],
    loginError: '', user: null,
    sites: [], overview: null, toasts: [],
    dragging: false, publishSlug: '', uploading: false, uploadPct: 0, uploadLabel: '',
    detail: null, versions: [], openFiles: {}, tab: 'versions',
    editPassword: '', clearPassword: false, editExpiry: '', editSlug: '',

    async init() {
      const params = new URLSearchParams(location.search);
      let pendingErr = params.get('login_error');
      const pendingUser = params.get('user') || '';
      if (pendingErr) {
        params.delete('login_error');
        params.delete('user');
        const qs = params.toString();
        history.replaceState(null, '', location.pathname + (qs ? '?' + qs : '') + location.hash);
      }
      try {
        const opt = await api('/auth/options');
        if (opt.ok) {
          const j = await opt.json();
          this.githubEnabled = !!j.github;
          this.apiKeyLogin = !!j.api_key;
          if (j.github_login) this.githubLoginURL = j.github_login;
          this.denyMessage = j.deny_message || '';
          this.allowlist = j.allowlist || [];
        }
      } catch (e) {}
      if (pendingErr) {
        this.loginError = this.formatLoginError(pendingErr, pendingUser);
      }
      try {
        await this.loadMe();
        if (this.authed) await this.refresh();
      } catch (e) {}
      this.ready = true;
    },
    get userLabel() {
      const u = this.user || {};
      if (u.method === 'github') return u.username || 'GitHub';
      if (u.method === 'apikey') return 'API 密钥';
      return '';
    },
    get userTitle() {
      const u = this.user || {};
      if (u.method === 'github') return 'GitHub: ' + (u.username || '');
      if (u.method === 'apikey') return 'API 密钥会话';
      return '';
    },
    get userInitial() {
      const l = this.userLabel || '?';
      return l.trim().slice(0, 1).toUpperCase();
    },
    async loadMe() {
      const r = await api('/auth/me');
      this.authed = r.ok;
      this.user = r.ok ? await r.json() : null;
    },
    formatLoginError(code, user) {
      if (code === 'not_allowed') {
        if (this.denyMessage) return this.denyMessage;
        const who = user ? ('「' + user + '」') : '该 GitHub 账号';
        const list = (this.allowlist && this.allowlist.length)
          ? this.allowlist.join(', ')
          : 'OPENNOW_GITHUB_ALLOWLIST';
        return who + ' 不在管理后台允许名单中（当前允许：' + list + '）。如需开通请联系管理员。';
      }
      return LOGIN_ERRORS[code] || ('登录失败：' + code);
    },
    async login() {
      this.busy = true;
      try {
        const r = await api('/auth/login', json({ api_key: this.apiKey }));
        if (!r.ok) return this.toast(await errMsg(r), false);
        this.apiKey = ''; this.loginError = '';
        await this.loadMe();
        this.toast('登录成功');
        await this.refresh();
      } finally { this.busy = false; }
    },
    async logout() {
      await api('/auth/logout', { method: 'POST' });
      this.authed = false; this.user = null; this.sites = []; this.overview = null;
    },
    async refresh() { await Promise.all([this.loadSites(), this.loadOverview()]); },
    backendLabel(b) {
      return ({ local: '本地磁盘', s3: 'S3 兼容', r2: 'Cloudflare R2', github: 'GitHub 仓库' })[b] || b || '—';
    },
    async loadOverview() { const r = await api('/storage/overview'); if (r.ok) this.overview = await r.json(); },
    async loadSites() {
      const q = this.query ? '?q=' + encodeURIComponent(this.query) : '';
      const r = await api('/sites' + q);
      if (r.status === 401) { this.authed = false; return; }
      if (!r.ok) return;
      const list = (await r.json()).sites || [];
      list.sort((a, b) => new Date(b.updated_at) - new Date(a.updated_at));
      this.sites = list;
    },

    // ---------- publish ----------
    async onDrop(ev) {
      this.dragging = false;
      const items = [...(ev.dataTransfer.items || [])];
      const entries = items.map(it => it.webkitGetAsEntry && it.webkitGetAsEntry()).filter(Boolean);
      if (entries.length) {
        const out = [];
        // A single dropped folder is published as the site root.
        const strip = entries.length === 1 && entries[0].isDirectory;
        for (const e of entries) await walkEntry(e, strip ? '' : null, out);
        return this.publishFiles(out);
      }
      const files = [...(ev.dataTransfer.files || [])].map(f => ({ file: f, path: f.name }));
      if (files.length) this.publishFiles(files);
    },
    onFiles(list, isDir) {
      const files = [...list].map(f => {
        let p = f.webkitRelativePath || f.name;
        if (isDir && p.includes('/')) p = p.slice(p.indexOf('/') + 1); // drop top folder name
        return { file: f, path: p };
      });
      if (files.length) this.publishFiles(files);
    },
    async publishFiles(files) {
      if (this.uploading) return;
      files = files.filter(f => !/(^|\/)\.DS_Store$/.test(f.path));
      if (!files.length) return this.toast('没有可上传的文件', false);
      this.uploading = true; this.uploadPct = 0;
      try {
        this.uploadLabel = '创建修订…';
        const cr = await api('/publish', json({ slug: this.publishSlug || '' }));
        if (!cr.ok) throw new Error(await httpErr(cr));
        const rev = await cr.json();
        const chunkSize = rev.chunk_size > 0 ? rev.chunk_size : (256 * 1024);
        const totalBytes = files.reduce((n, f) => n + (f.file.size || 0), 0) || 1;
        let sentBytes = 0;
        for (const f of files) {
          this.uploadLabel = `上传 ${f.path}`;
          await this.uploadFileChunked(rev.revision_id, f.path, f.file, chunkSize, (n) => {
            sentBytes += n;
            this.uploadPct = Math.min(95, Math.round(sentBytes / totalBytes * 95));
          });
        }
        this.uploadLabel = '上线中…';
        const fin = await api('/publish/' + rev.revision_id + '/finalize', { method: 'POST' });
        if (!fin.ok) throw new Error(await httpErr(fin));
        const out = await fin.json();
        this.uploadPct = 100;
        this.toast(`已发布 ${out.slug}（${files.length} 个文件）`);
        this.publishSlug = '';
        await this.refresh();
      } catch (e) {
        this.toast('发布失败：' + formatUploadError(e), false);
      } finally {
        setTimeout(() => { this.uploading = false; }, 600);
      }
    },
    async uploadFileChunked(revisionId, path, file, chunkSize, onBytes) {
      const total = file.size || 0;
      if (total === 0) {
        const r = await api('/publish/' + revisionId + '/files/' + encPath(path), {
          method: 'PUT', body: new Blob(),
          headers: { 'Content-Type': 'application/octet-stream', 'X-OpenNow-Offset': '0', 'X-OpenNow-Total': '0' },
        });
        if (!r.ok) throw Object.assign(new Error(await httpErr(r)), { status: r.status });
        return;
      }
      let offset = 0;
      while (offset < total) {
        const end = Math.min(offset + chunkSize, total);
        const blob = file.slice(offset, end);
        const r = await api('/publish/' + revisionId + '/files/' + encPath(path), {
          method: 'PUT',
          body: blob,
          headers: {
            'Content-Type': 'application/octet-stream',
            'X-OpenNow-Offset': String(offset),
            'X-OpenNow-Total': String(total),
          },
        });
        if (!r.ok) throw Object.assign(new Error(`${path}: ${await httpErr(r)}`), { status: r.status });
        const n = end - offset;
        offset = end;
        if (onBytes) onBytes(n);
      }
    },

    // ---------- site detail ----------
    async openSite(slug, keepTab) {
      const s = encodeURIComponent(slug);
      const [d, v] = await Promise.all([api('/sites/' + s), api('/sites/' + s + '/versions')]);
      if (!d.ok) return this.toast(await errMsg(d), false);
      this.detail = await d.json();
      this.versions = v.ok ? ((await v.json()).versions || []) : [];
      this.openFiles = {};
      if (!keepTab) this.tab = 'versions';
      this.editPassword = ''; this.clearPassword = false;
      this.editExpiry = toLocalInput(this.detail.meta.expires_at);
      this.editSlug = this.detail.meta.slug;
      if (!this.$refs.drawer.open) this.$refs.drawer.showModal();
    },
    closeDrawer() { this.$refs.drawer.close(); },
    async toggleFiles(v) {
      if (this.openFiles[v.id]) { const o = { ...this.openFiles }; delete o[v.id]; this.openFiles = o; return; }
      const r = await api('/sites/' + encodeURIComponent(this.detail.meta.slug) + '/versions/' + encodeURIComponent(v.id) + '/files');
      if (!r.ok) return this.toast(await errMsg(r), false);
      this.openFiles = { ...this.openFiles, [v.id]: (await r.json()).files || [] };
    },
    fileUrl(v, path) {
      const [base, q] = v.preview_url.split('?');
      return base + encPath(path) + (q ? '?' + q : '');
    },
    async saveAccess() {
      const slug = this.detail.meta.slug;
      const body = {};
      const nextSlug = (this.editSlug || '').trim();
      if (nextSlug && nextSlug !== slug) body.slug = nextSlug;
      if (this.clearPassword) body.clear_password = true;
      else if (this.editPassword) body.password = this.editPassword;
      if (this.editExpiry) body.expires_at = new Date(this.editExpiry).toISOString();
      else body.clear_expiry = true;
      const r = await api('/sites/' + encodeURIComponent(slug), json(body, 'PATCH'));
      if (!r.ok) return this.toast(await errMsg(r), false);
      const saved = await r.json();
      const newSlug = (saved.meta && saved.meta.slug) || nextSlug || slug;
      this.toast(newSlug !== slug ? `已重命名为 ${newSlug}` : '访问设置已保存');
      await this.refresh();
      await this.openSite(newSlug, true);
    },
    async rollback(versionId) {
      const slug = this.detail.meta.slug;
      if (!confirm(`将 ${slug} 的线上版本切换为 ${this.short(versionId)}？`)) return;
      const r = await api('/sites/' + encodeURIComponent(slug) + '/rollback', json({ version_id: versionId }));
      if (!r.ok) return this.toast(await errMsg(r), false);
      this.toast('已切换线上版本');
      await this.refresh();
      await this.openSite(slug, true);
    },
    async deleteSite(slug) {
      if (!confirm(`确定删除站点「${slug}」及其全部版本？此操作不可恢复。`)) return;
      const r = await api('/sites/' + encodeURIComponent(slug), { method: 'DELETE' });
      if (!r.ok) return this.toast(await errMsg(r), false);
      this.toast('已删除 ' + slug);
      await this.refresh();
    },

    // ---------- helpers ----------
    siteUrl(slug) { return '/' + encodeURIComponent(slug) + '/'; },
    short(id) { return id ? id.slice(0, 8) : '—'; },
    isExpired(s) { return !!s.expires_at && new Date(s.expires_at) < new Date(); },
    formatTime(t) { if (!t) return '—'; try { return new Date(t).toLocaleString('zh-CN', { hour12: false }); } catch { return t; } },
    formatDate(t) { try { return new Date(t).toLocaleDateString('zh-CN'); } catch { return t; } },
    formatBytes(n) {
      if (n < 1024) return n + ' B';
      if (n < 1024 * 1024) return (n / 1024).toFixed(1) + ' KB';
      if (n < 1024 ** 3) return (n / 1024 / 1024).toFixed(1) + ' MB';
      return (n / 1024 ** 3).toFixed(2) + ' GB';
    },
    toast(msg, ok = true) {
      const id = Date.now() + Math.random();
      this.toasts.push({ id, msg, ok });
      setTimeout(() => { this.toasts = this.toasts.filter(t => t.id !== id); }, ok ? 2800 : 5000);
    },
  };
}

// Recursively collect files from a dropped FileSystemEntry.
// prefix === null keeps the entry's own name; '' strips it (folder as root).
async function walkEntry(entry, prefix, out) {
  const name = prefix === null ? entry.name : prefix;
  if (entry.isFile) {
    const file = await new Promise((res, rej) => entry.file(res, rej));
    out.push({ file, path: name || entry.name });
    return;
  }
  const reader = entry.createReader();
  let batch;
  do {
    batch = await new Promise((res, rej) => reader.readEntries(res, rej));
    for (const child of batch) await walkEntry(child, name ? name + '/' + child.name : child.name, out);
  } while (batch.length);
}

function toLocalInput(t) {
  if (!t) return '';
  const d = new Date(t);
  const pad = (n) => String(n).padStart(2, '0');
  return `${d.getFullYear()}-${pad(d.getMonth() + 1)}-${pad(d.getDate())}T${pad(d.getHours())}:${pad(d.getMinutes())}`;
}

async function errMsg(r) {
  try { const j = await r.json(); return j.error || r.statusText; } catch { return r.statusText; }
}

async function httpErr(r) {
  const msg = await errMsg(r);
  if (r.status === 408 || r.status === 504 || r.status === 524) {
    return `上传超时（HTTP ${r.status}）：${msg}。请重试；大文件已自动分块。`;
  }
  if (r.status >= 400 && r.status < 500) {
    return `请求被拒绝（HTTP ${r.status}）：${msg}`;
  }
  if (r.status >= 500) {
    return `服务端错误（HTTP ${r.status}）：${msg}`;
  }
  return msg;
}

function formatUploadError(e) {
  const m = (e && e.message) ? String(e.message) : String(e);
  if (e && e.name === 'AbortError') return '上传被中断或超时，请重试';
  if (/failed to fetch|networkerror|load failed|timeout/i.test(m)) {
    return '网络中断或代理超时（常见于 Cloudflare Tunnel 大文件）。已改为分块上传，请重试；若仍失败请检查隧道延迟。详情：' + m;
  }
  return m;
}
