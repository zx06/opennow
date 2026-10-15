// opennow admin — Alpine.js component (vendored, no build step).
function adminApp() {
  const api = (p, opt) => fetch('/api/v1' + p, Object.assign({ credentials: 'same-origin' }, opt));
  const json = (body, method = 'POST') => ({ method, headers: { 'Content-Type': 'application/json' }, body: JSON.stringify(body) });
  const encPath = (p) => p.split('/').map(encodeURIComponent).join('/');

  return {
    ready: false, authed: false, busy: false, apiKey: '', query: '',
    sites: [], overview: null, toasts: [],
    dragging: false, publishSlug: '', uploading: false, uploadPct: 0, uploadLabel: '',
    detail: null, versions: [], openFiles: {}, tab: 'versions',
    editPassword: '', clearPassword: false, editExpiry: '',

    async init() {
      try { const r = await api('/auth/me'); this.authed = r.ok; if (this.authed) await this.refresh(); } catch (e) {}
      this.ready = true;
    },
    async login() {
      this.busy = true;
      try {
        const r = await api('/auth/login', json({ api_key: this.apiKey }));
        if (!r.ok) return this.toast(await errMsg(r), false);
        this.authed = true; this.apiKey = '';
        this.toast('登录成功');
        await this.refresh();
      } finally { this.busy = false; }
    },
    async logout() {
      await api('/auth/logout', { method: 'POST' });
      this.authed = false; this.sites = []; this.overview = null;
    },
    async refresh() { await Promise.all([this.loadSites(), this.loadOverview()]); },
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
        if (!cr.ok) throw new Error(await errMsg(cr));
        const rev = await cr.json();
        let done = 0;
        for (const f of files) {
          this.uploadLabel = `上传 ${f.path}`;
          const r = await api('/publish/' + rev.revision_id + '/files/' + encPath(f.path), { method: 'PUT', body: f.file });
          if (!r.ok) throw new Error(`${f.path}: ${await errMsg(r)}`);
          done++; this.uploadPct = Math.round(done / files.length * 95);
        }
        this.uploadLabel = '上线中…';
        const fin = await api('/publish/' + rev.revision_id + '/finalize', { method: 'POST' });
        if (!fin.ok) throw new Error(await errMsg(fin));
        const out = await fin.json();
        this.uploadPct = 100;
        this.toast(`已发布 ${out.slug}（${files.length} 个文件）`);
        this.publishSlug = '';
        await this.refresh();
      } catch (e) {
        this.toast('发布失败：' + (e.message || e), false);
      } finally {
        setTimeout(() => { this.uploading = false; }, 600);
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
      if (this.clearPassword) body.clear_password = true;
      else if (this.editPassword) body.password = this.editPassword;
      if (this.editExpiry) body.expires_at = new Date(this.editExpiry).toISOString();
      else body.clear_expiry = true;
      const r = await api('/sites/' + encodeURIComponent(slug), json(body, 'PATCH'));
      if (!r.ok) return this.toast(await errMsg(r), false);
      this.toast('访问设置已保存');
      await this.refresh();
      await this.openSite(slug, true);
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
