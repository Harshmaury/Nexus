// @nexus-project: nexus
// @nexus-path: cmd/engx/cmd_ui.go
// engx ui — starts a local web interface on 127.0.0.1:7070.
//
// Architecture:
//   - Single static HTML page embedded in the binary (no external assets)
//   - Proxy routes under /api/* forward to engxd, Forge, Guardian
//     so the browser never makes cross-origin requests
//   - Read-only: all proxied calls are GET only
//   - No auth required for local dev (same as engxd with no service-tokens)
//
// Usage:
//   engx ui              (opens browser automatically)
//   engx ui --no-open    (start server only)
//   engx ui --port 7071  (custom port)
package main

import (
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"os/exec"
	"os/signal"
	"runtime"
	"strings"
	"syscall"
	"time"

	"github.com/spf13/cobra"
)

func uiCmd(httpAddr *string) *cobra.Command {
	var port int
	var noOpen bool
	cmd := &cobra.Command{
		Use:   "ui",
		Short: "Open the platform web interface",
		RunE: func(cmd *cobra.Command, args []string) error {
			return runUI(*httpAddr, port, noOpen)
		},
	}
	cmd.Flags().IntVar(&port, "port", 7070, "port to serve the UI on")
	cmd.Flags().BoolVar(&noOpen, "no-open", false, "do not open browser automatically")
	return cmd
}

func runUI(nexusAddr string, port int, noOpen bool) error {
	addr := fmt.Sprintf("127.0.0.1:%d", port)

	// Verify engxd is reachable before starting.
	client := &http.Client{Timeout: 2 * time.Second}
	if _, err := client.Get(nexusAddr + "/health"); err != nil {
		return fmt.Errorf("engxd not running — start with: engxd &")
	}

	mux := http.NewServeMux()

	// ── Static UI ──────────────────────────────────────────────────────────
	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/" {
			http.NotFound(w, r)
			return
		}
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		w.Header().Set("Cache-Control", "no-cache")
		fmt.Fprint(w, uiHTML)
	})

	// ── API proxy ──────────────────────────────────────────────────────────
	// /api/nexus/*   → nexusAddr/*
	// /api/forge/*   → http://127.0.0.1:8082/*
	// /api/guardian/ → http://127.0.0.1:8085/guardian/findings
	// /api/mode      → nexusAddr/system/mode
	mux.HandleFunc("/api/", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet {
			http.Error(w, "read-only proxy", http.StatusMethodNotAllowed)
			return
		}
		w.Header().Set("Access-Control-Allow-Origin", "*")
		w.Header().Set("Content-Type", "application/json")

		path := strings.TrimPrefix(r.URL.Path, "/api/")
		query := ""
		if r.URL.RawQuery != "" {
			query = "?" + r.URL.RawQuery
		}

		var upstream string
		switch {
		case strings.HasPrefix(path, "nexus/"):
			upstream = nexusAddr + "/" + strings.TrimPrefix(path, "nexus/") + query
		case strings.HasPrefix(path, "forge/"):
			upstream = "http://127.0.0.1:8082/" + strings.TrimPrefix(path, "forge/") + query
		case strings.HasPrefix(path, "guardian/"):
			upstream = "http://127.0.0.1:8085/" + path + query
		default:
			http.NotFound(w, r)
			return
		}

		resp, err := client.Get(upstream)
		if err != nil {
			http.Error(w, `{"ok":false,"error":"upstream unavailable"}`, http.StatusBadGateway)
			return
		}
		defer resp.Body.Close()
		w.WriteHeader(resp.StatusCode)
		io.Copy(w, resp.Body) //nolint:errcheck
	})

	srv := &http.Server{
		Addr:         addr,
		Handler:      mux,
		ReadTimeout:  10 * time.Second,
		WriteTimeout: 10 * time.Second,
	}

	// Check port is free.
	ln, err := net.Listen("tcp", addr)
	if err != nil {
		return fmt.Errorf("port %d in use — try: engx ui --port %d", port, port+1)
	}

	url := fmt.Sprintf("http://%s", addr)
	fmt.Printf("  engx ui → %s\n", url)
	fmt.Println("  Press Ctrl+C to stop")

	if !noOpen {
		go openUIBrowser(url)
	}

	sigCh := make(chan os.Signal, 1)
	signal.Notify(sigCh, syscall.SIGINT, syscall.SIGTERM)
	go func() {
		<-sigCh
		fmt.Println("\n  UI server stopped")
		srv.Close()
	}()

	if err := srv.Serve(ln); err != nil && err != http.ErrServerClosed {
		return fmt.Errorf("ui server: %w", err)
	}
	return nil
}

func openUIBrowser(url string) {
	time.Sleep(300 * time.Millisecond)
	var cmd string
	switch runtime.GOOS {
	case "windows":
		cmd = "cmd"
	case "darwin":
		cmd = "open"
	default:
		cmd = "xdg-open"
	}
	if runtime.GOOS == "windows" {
		exec.Command(cmd, "/c", "start", url).Start() //nolint:errcheck
	} else {
		exec.Command(cmd, url).Start() //nolint:errcheck
	}
}

// uiHTML is the complete single-page UI embedded in the binary.
// Calls /api/* proxy routes — never calls engxd directly.
const uiHTML = `<!DOCTYPE html>
<html lang="en">
<head>
<meta charset="UTF-8">
<meta name="viewport" content="width=device-width,initial-scale=1">
<title>engx platform</title>
<style>
*{box-sizing:border-box;margin:0;padding:0}
body{font-family:-apple-system,BlinkMacSystemFont,'Segoe UI',sans-serif;font-size:14px;background:#f8f8f6;color:#1a1a1a;height:100vh;display:flex;flex-direction:column}
a{color:inherit;text-decoration:none}
.topbar{background:#fff;border-bottom:1px solid #e8e8e4;padding:0 20px;height:44px;display:flex;align-items:center;gap:16px;flex-shrink:0}
.topbar-title{font-size:13px;font-weight:600;letter-spacing:.01em}
.mode-badge{font-size:11px;padding:2px 8px;border-radius:10px;font-weight:500}
.mode-full{background:#eaf3de;color:#3b6d11}
.mode-insecure{background:#faeeda;color:#854f0b}
.mode-degraded{background:#e6f1fb;color:#185fa5}
.layout{display:flex;flex:1;overflow:hidden}
.sidebar{width:210px;background:#fff;border-right:1px solid #e8e8e4;overflow-y:auto;padding:12px 0;flex-shrink:0}
.sidebar-label{font-size:10px;font-weight:600;color:#888;letter-spacing:.08em;text-transform:uppercase;padding:0 14px 6px}
.proj-item{display:flex;align-items:center;gap:8px;padding:6px 14px;cursor:pointer;font-size:13px;color:#444;border-left:2px solid transparent}
.proj-item:hover{background:#f4f4f0}
.proj-item.active{background:#f0f0ec;color:#1a1a1a;font-weight:500;border-left-color:#1a1a1a}
.dot{width:7px;height:7px;border-radius:50%;flex-shrink:0}
.d-ok{background:#639922}.d-warn{background:#BA7517}.d-err{background:#E24B4A}.d-stop{background:#ccc}
.main{flex:1;overflow-y:auto;padding:24px}
.proj-header{display:flex;align-items:center;gap:12px;margin-bottom:16px}
.proj-title{font-size:17px;font-weight:600}
.tabs{display:flex;gap:0;border-bottom:1px solid #e8e8e4;margin-bottom:20px}
.tab{padding:7px 14px;font-size:13px;cursor:pointer;color:#666;border-bottom:2px solid transparent;margin-bottom:-1px}
.tab.active{color:#1a1a1a;border-bottom-color:#1a1a1a;font-weight:500}
.panel{display:none}.panel.active{display:block}
.verdict{display:flex;align-items:center;gap:10px;padding:10px 14px;border-radius:8px;margin-bottom:16px;font-size:13px;border:1px solid}
.v-ok{background:#eaf3de;border-color:#c0dd97;color:#3b6d11}
.v-err{background:#fcebeb;border-color:#f7c1c1;color:#a32d2d}
.v-warn{background:#faeeda;border-color:#fac775;color:#854f0b}
.v-icon{font-size:14px;width:18px}
.v-label{font-weight:600}
.v-sub{margin-left:auto;font-size:12px;opacity:.8}
.cards{display:grid;grid-template-columns:repeat(3,1fr);gap:8px;margin-bottom:20px}
.card{background:#fff;border:1px solid #e8e8e4;border-radius:8px;padding:12px 14px}
.card-label{font-size:10px;color:#888;text-transform:uppercase;letter-spacing:.06em;margin-bottom:6px}
.card-val{font-size:18px;font-weight:600;line-height:1.2}
.card-sub{font-size:11px;color:#888;margin-top:4px}
.findings{margin-bottom:20px}
.findings-label{font-size:11px;font-weight:600;color:#888;letter-spacing:.06em;text-transform:uppercase;margin-bottom:8px}
.finding{display:flex;gap:10px;padding:9px 12px;border-radius:6px;margin-bottom:5px;font-size:12px;border:1px solid}
.f-err{background:#fcebeb;border-color:#f7c1c1}
.f-warn{background:#faeeda;border-color:#fac775}
.f-info{background:#f4f4f0;border-color:#e8e8e4}
.f-ok{background:#eaf3de;border-color:#c0dd97}
.f-rule{font-weight:600;min-width:44px;color:#555;flex-shrink:0}
.f-msg{color:#1a1a1a;line-height:1.4}
.actions-label{font-size:11px;font-weight:600;color:#888;letter-spacing:.06em;text-transform:uppercase;margin-bottom:8px}
.action-btn{font-size:12px;padding:5px 10px;border:1px solid #d0d0c8;border-radius:6px;background:#fff;color:#1a1a1a;cursor:pointer;margin-right:6px;font-family:monospace}
.action-btn:hover{background:#f4f4f0}
.timeline{border-left:2px solid #e8e8e4;padding-left:14px;margin-left:8px}
.t-item{position:relative;margin-bottom:11px;font-size:12px;line-height:1.5}
.t-dot{position:absolute;left:-19px;top:4px;width:8px;height:8px;border-radius:50%;border:2px solid #ccc;background:#fff}
.t-ok .t-dot{border-color:#639922;background:#eaf3de}
.t-err .t-dot{border-color:#E24B4A;background:#fcebeb}
.t-deny .t-dot{border-color:#BA7517;background:#faeeda}
.t-time{color:#999;margin-right:8px}
.t-svc{font-weight:500;margin-right:6px}
.t-badge{font-size:10px;padding:1px 6px;border-radius:10px;display:inline-block}
.b-ok{background:#eaf3de;color:#3b6d11}
.b-err{background:#fcebeb;color:#a32d2d}
.b-deny{background:#faeeda;color:#854f0b}
.t-meta{color:#999;font-size:11px;margin-left:6px}
.empty{color:#999;font-size:13px;padding:12px 0}
.section-hd{font-size:11px;font-weight:600;color:#888;letter-spacing:.06em;text-transform:uppercase;margin-bottom:10px}
.refresh-btn{margin-left:auto;font-size:11px;padding:4px 10px;border:1px solid #d0d0c8;border-radius:6px;background:#fff;cursor:pointer;color:#555}
.refresh-btn:hover{background:#f4f4f0}
.loading{color:#999;font-size:13px;padding:8px 0}
</style>
</head>
<body>

<div class="topbar">
  <div class="topbar-title">engx platform</div>
  <div class="mode-badge" id="mode-badge">loading</div>
  <button class="refresh-btn" onclick="reload()">refresh</button>
</div>

<div class="layout">
  <div class="sidebar">
    <div class="sidebar-label">Projects</div>
    <div id="proj-list"><div style="padding:10px 14px;font-size:12px;color:#999">loading...</div></div>
  </div>
  <div class="main" id="main">
    <div class="loading">Loading platform data...</div>
  </div>
</div>

<script>
const API = '';
let S = {projects:[],services:[],findings:[],history:{},events:[],mode:'loading',sel:null};

async function get(path) {
  try {
    const r = await fetch(API+path,{signal:AbortSignal.timeout(3000)});
    if(!r.ok) return null;
    return r.json();
  } catch { return null; }
}

async function reload() {
  document.getElementById('main').innerHTML = '<div class="loading">Refreshing...</div>';
  await load();
}

async function load() {
  const [proj,svc,guard,hist,ev,mode] = await Promise.all([
    get('/api/nexus/projects'),
    get('/api/nexus/services'),
    get('/api/guardian/findings'),
    get('/api/forge/history?limit=200'),
    get('/api/nexus/events?limit=200'),
    get('/api/nexus/system/mode'),
  ]);

  S.projects = proj?.data || [];
  S.services  = svc?.data  || [];
  S.findings  = guard?.data?.findings || [];
  S.events    = ev?.data   || [];
  S.mode      = mode?.data?.mode || 'unknown';

  const hmap = {};
  (hist?.data||[]).forEach(h=>{
    if(!hmap[h.target]) hmap[h.target]=[];
    if(hmap[h.target].length<15) hmap[h.target].push(h);
  });
  S.history = hmap;

  renderMode();
  renderSidebar();
  if(S.sel) select(S.sel);
  else if(S.projects.length) select(S.projects[0].id||S.projects[0].name);
}

function renderMode() {
  const b = document.getElementById('mode-badge');
  b.textContent = S.mode;
  b.className = 'mode-badge mode-'+S.mode;
}

function dotClass(pid) {
  const svcs = S.services.filter(s=>s.project===pid);
  if(!svcs.length) return 'd-stop';
  if(svcs.some(s=>s.actual_state==='maintenance')) return 'd-err';
  if(svcs.some(s=>s.actual_state!=='running'&&s.desired_state==='running')) return 'd-warn';
  return 'd-ok';
}

function renderSidebar() {
  document.getElementById('proj-list').innerHTML =
    S.projects.map(function(p){
    return '<div class="proj-item" id="pi-'+p.id+'" onclick="select(\u0022'+p.id+'\u0022)">'
      +'<div class="dot '+dotClass(p.id)+'"></div>'+(p.name||p.id)+'</div>';
  }).join('');
}

function select(id) {
  S.sel = id;
  document.querySelectorAll('.proj-item').forEach(e=>e.classList.remove('active'));
  const el = document.getElementById('pi-'+id);
  if(el) el.classList.add('active');
  renderMain(id);
}

function tab(name) {
  document.querySelectorAll('.tab').forEach(e=>e.classList.remove('active'));
  document.querySelectorAll('.panel').forEach(e=>e.classList.remove('active'));
  document.getElementById('tab-'+name).classList.add('active');
  document.getElementById('panel-'+name).classList.add('active');
}

function renderMain(id) {
  const svcs = S.services.filter(s=>s.project===id);
  const hist = S.history[id]||[];
  const pFinds = S.findings.filter(f=>f.target===id||f.target==='platform');
  const errors = pFinds.filter(f=>f.severity==='error');
  const warns  = pFinds.filter(f=>f.severity==='warning');
  const last   = hist[0];
  const svc    = svcs[0];

  let vc='v-ok',vi='✓',vl='Healthy',vs='No issues found';
  if(errors.length){vc='v-err';vi='✗';vl='Needs attention';vs=errors.length+' error(s)';}
  else if(warns.length){vc='v-warn';vi='○';vl='Warnings';vs=warns.length+' warning(s)';}

  const okCount = hist.filter(h=>h.status==='success').length;
  const svcState = svc?.actual_state||'—';
  const failCnt  = svc?.fail_count||0;

  document.getElementById('main').innerHTML = [
    '<div class="proj-header">
      <div class="proj-title">${id}</div>
    </div>
    <div class="tabs">
      <div class="tab active" id="tab-why" onclick="tab('why')">Why</div>
      <div class="tab" id="tab-history" onclick="tab('history')">History</div>
      <div class="tab" id="tab-activity" onclick="tab('activity')">Activity</div>
    </div>

    <div class="panel active" id="panel-why">
      <div class="verdict ${vc}">
        <div class="v-icon">${vi}</div>
        <div class="v-label">${vl}</div>
        <div class="v-sub">${vs}</div>
      </div>
      <div class="cards">
        <div class="card">
          <div class="card-label">Service state</div>
          <div class="card-val" style="font-size:15px">${svcState}</div>
          <div class="card-sub">${failCnt>0?failCnt+' failure(s)':'fail count 0'}</div>
        </div>
        <div class="card">
          <div class="card-label">Builds (last 15)</div>
          <div class="card-val">${okCount}<span style="font-size:13px;font-weight:400;color:#888">/${hist.length}</span></div>
          <div class="card-sub">succeeded</div>
        </div>
        <div class="card">
          <div class="card-label">Last execution</div>
          <div class="card-val" style="font-size:14px">${last?ago(last.started_at):'—'}</div>
          <div class="card-sub">${last?last.status:'never'}</div>
        </div>
      </div>
      <div class="findings">
        <div class="findings-label">Guardian findings</div>
        '+(pFinds.length ? pFinds.map(function(f){return '<div class="finding '+(f.severity==="error"?"f-err":f.severity==="warning"?"f-warn":f.severity==="info"?"f-info":"f-ok")+'">'+'<div class="f-rule">'+f.rule_id+'</div><div class="f-msg">'+f.message+'</div></div>';}).join('') : '<div class="finding f-ok"><div class="f-rule">—</div><div class="f-msg">No findings for this project</div></div>')+'
      </div>
      <div>
        <div class="actions-label">Quick commands</div>
        <button class="action-btn" onclick="copy('engx why ${id}')">engx why ${id}</button>
        <button class="action-btn" onclick="copy('engx check ${id}')">engx check ${id}</button>
        <button class="action-btn" onclick="copy('engx build ${id}')">engx build ${id}</button>
        <button class="action-btn" onclick="copy('engx logs ${svc?.id||id+'-daemon'} --since-crash')">logs --since-crash</button>
      </div>
    </div>

    <div class="panel" id="panel-history">
      <div class="section-hd">Execution history — ${id}</div>
      '+(hist.length ? '<div class="timeline">'+hist.map(function(h){
        const cls = h.status==='success'?'t-ok':h.status==='denied'?'t-deny':'t-err';
        const bc  = h.status==='success'?'b-ok':h.status==='denied'?'b-deny':'b-err';
        const dur = h.duration_ms>1000?(h.duration_ms/1000).toFixed(1)+'s':h.duration_ms>1?h.duration_ms+'ms':'';
        return '<div class="t-item '+cls+'"><div class="t-dot"></div>'+
          '<span class="t-time">'+fmtTime(h.started_at)+'</span>'+
          '<span class="t-svc">'+h.intent+'</span>'+
          '<span class="t-badge '+bc+'">'+h.status+'</span>'+
          (dur?'<span class="t-meta">'+dur+'</span>':'')+
          '<span class="t-meta">'+(h.actor_sub||'anonymous')+'</span>'+
          return '</div>';}).join('')+'</div>' : '<div class="empty">No execution history for this project.</div>')+'
    </div>

    <div class="panel" id="panel-activity">
      <div class="section-hd">Platform activity</div>
      ${buildActivity()}
    </div>'].join('');
}

function buildActivity() {
  const labels = {SERVICE_STARTED:'started',SERVICE_STOPPED:'stopped',SERVICE_CRASHED:'crashed',SERVICE_HEALED:'healed',STATE_CHANGED:'state changed',SYSTEM_ALERT:'alert'};
  const entries = S.events
    .filter(e=>labels[e.type])
    .slice(0,40)
    .map(e=>({at:e.created_at,svc:e.service_id,label:labels[e.type],err:e.type==='SERVICE_CRASHED'}));
  if(!entries.length) return '<div class="empty">No recent activity.</div>';
  return '<div class="timeline">'+entries.map(function(e){return '<div class="t-item '+(e.err?'t-err':'t-ok')+'">'+'<div class="t-dot"></div>'+'<span class="t-time">'+fmtTime(e.at)+'</span>'+'<span class="t-svc">'+e.svc+'</span>'+'<span class="t-badge '+(e.err?'b-err':'b-ok')+'">'+e.label+'</span>'+'</div>';}).join('')+'</div>';
}

function fmtTime(iso){try{return new Date(iso).toLocaleTimeString('en-GB',{hour:'2-digit',minute:'2-digit'});}catch{return'—';}}
function ago(iso){try{const d=Date.now()-new Date(iso);const h=Math.floor(d/3600000),m=Math.floor((d%3600000)/60000);return h>0?h+'h '+m+'m ago':m+'m ago';}catch{return'—';}}

function copy(cmd) {
  if(navigator.clipboard) navigator.clipboard.writeText(cmd).then(()=>{ const b=event.target; const orig=b.textContent; b.textContent='copied!'; setTimeout(()=>b.textContent=orig,1200); });
}

load();
</script>
</body>
</html>`
