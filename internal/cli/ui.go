package cli

import (
	"encoding/json"
	"fmt"
	"html/template"
	"net"
	"net/http"
	"strings"

	"github.com/getbx/bx/internal/install"
	"github.com/urfave/cli/v2"
)

type uiServer struct {
	host      string
	sharesDir string
}

type shareView struct {
	Name   string `json:"name"`
	Listen string `json:"listen"`
	Status string `json:"status"`
}

func serverUIAction(c *cli.Context) error {
	if !isLoopbackListen(c.String("listen")) {
		return fmt.Errorf("server ui 只允许监听本机地址,例如 127.0.0.1:8787")
	}
	s := uiServer{host: c.String("host"), sharesDir: c.String("shares-dir")}
	mux := http.NewServeMux()
	mux.HandleFunc("/", s.handleIndex)
	mux.HandleFunc("/api/shares", s.handleShares)
	mux.HandleFunc("/api/share", s.handleShare)
	mux.HandleFunc("/api/revoke", s.handleRevoke)
	fmt.Printf("bx server ui: http://%s\n", c.String("listen"))
	fmt.Println("Tip: keep it on 127.0.0.1 and access it through SSH port forwarding.")
	return http.ListenAndServe(c.String("listen"), mux)
}

func isLoopbackListen(addr string) bool {
	host, _, err := net.SplitHostPort(addr)
	if err != nil {
		return false
	}
	if host == "localhost" {
		return true
	}
	ip := net.ParseIP(host)
	return ip != nil && ip.IsLoopback()
}

func (s uiServer) handleIndex(w http.ResponseWriter, r *http.Request) {
	if r.URL.Path != "/" {
		http.NotFound(w, r)
		return
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	_ = uiTemplate.Execute(w, map[string]string{"Host": s.host})
}

func (s uiServer) handleShares(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	shares, err := s.shareViews()
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	writeHTTPJSON(w, shares)
}

func (s uiServer) handleShare(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	name, err := cleanShareName(r.FormValue("name"))
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	host := strings.TrimSpace(r.FormValue("host"))
	if host == "" {
		host = s.host
	}
	if host == "" {
		http.Error(w, "host is required", http.StatusBadRequest)
		return
	}
	link, listen, err := createShare(name, host, s.sharesDir, "", "")
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	writeHTTPJSON(w, map[string]string{"name": name, "listen": listen, "link": link})
}

func (s uiServer) handleRevoke(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	name, err := cleanShareName(r.FormValue("name"))
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	if err := revokeShare(name, s.sharesDir); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	writeHTTPJSON(w, map[string]string{"ok": "true"})
}

func (s uiServer) shareViews() ([]shareView, error) {
	shares, err := readShares(s.sharesDir)
	if err != nil {
		return nil, err
	}
	return shareViews(shares), nil
}

func shareViews(shares []shareInfo) []shareView {
	out := make([]shareView, 0, len(shares))
	for _, share := range shares {
		out = append(out, shareView{
			Name:   share.Name,
			Listen: share.Config.Listen,
			Status: systemctlState("is-active", install.ShareServiceName(share.Name)),
		})
	}
	return out
}

func writeHTTPJSON(w http.ResponseWriter, v any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	_ = json.NewEncoder(w).Encode(v)
}

var uiTemplate = template.Must(template.New("ui").Parse(`<!doctype html>
<html lang="en">
<head>
<meta charset="utf-8">
<meta name="viewport" content="width=device-width,initial-scale=1">
<title>bx server</title>
<style>
body{font-family:-apple-system,BlinkMacSystemFont,Segoe UI,sans-serif;margin:0;background:#f6f7f9;color:#15171a}
main{max-width:920px;margin:40px auto;padding:0 20px}
h1{font-size:28px;margin:0 0 24px}
section{background:white;border:1px solid #e4e7ec;border-radius:8px;padding:18px;margin:14px 0}
label{display:block;font-size:13px;color:#555;margin:10px 0 4px}
input{box-sizing:border-box;width:100%;padding:10px;border:1px solid #cfd5df;border-radius:6px;font-size:15px}
button{border:0;background:#111827;color:white;border-radius:6px;padding:10px 14px;font-size:14px;cursor:pointer}
button.secondary{background:#e5e7eb;color:#111827}
button.danger{background:#b42318}
table{width:100%;border-collapse:collapse}
th,td{text-align:left;border-bottom:1px solid #edf0f3;padding:10px 6px;font-size:14px}
code,textarea{font-family:ui-monospace,SFMono-Regular,Menlo,monospace}
textarea{box-sizing:border-box;width:100%;min-height:88px;margin-top:10px;padding:10px;border:1px solid #cfd5df;border-radius:6px}
.row{display:grid;grid-template-columns:1fr 1fr auto;gap:10px;align-items:end}
.muted{color:#6b7280;font-size:13px}
.error{color:#b42318}
</style>
</head>
<body>
<main>
<h1>bx server</h1>
<section>
<div class="row">
<div><label>Name</label><input id="name" placeholder="alice"></div>
<div><label>Host</label><input id="host" value="{{.Host}}" placeholder="vps.example.com"></div>
<button onclick="createShare()">Create</button>
</div>
<textarea id="link" readonly placeholder="bx:// link appears here"></textarea>
<p id="msg" class="muted"></p>
</section>
<section>
<table>
<thead><tr><th>Name</th><th>Listen</th><th>Status</th><th></th></tr></thead>
<tbody id="shares"></tbody>
</table>
</section>
</main>
<script>
async function api(path, options){const r=await fetch(path,options); if(!r.ok) throw new Error(await r.text()); return await r.json();}
async function loadShares(){
  const rows=await api('/api/shares');
  const body=document.getElementById('shares');
  body.innerHTML='';
  if(rows.length===0){body.innerHTML='<tr><td colspan="4" class="muted">No shares.</td></tr>';return;}
  for(const s of rows){
    const tr=document.createElement('tr');
    tr.innerHTML='<td></td><td></td><td></td><td><button class="danger">Revoke</button></td>';
    tr.children[0].textContent=s.name; tr.children[1].textContent=s.listen; tr.children[2].textContent=s.status;
    tr.querySelector('button').onclick=()=>revokeShare(s.name);
    body.appendChild(tr);
  }
}
async function createShare(){
  const fd=new FormData(); fd.set('name',document.getElementById('name').value); fd.set('host',document.getElementById('host').value);
  try{const r=await api('/api/share',{method:'POST',body:fd}); document.getElementById('link').value=r.link; document.getElementById('msg').textContent='Created '+r.name+' on '+r.listen; await loadShares();}
  catch(e){document.getElementById('msg').innerHTML='<span class="error">'+e.message+'</span>';}
}
async function revokeShare(name){
  const fd=new FormData(); fd.set('name',name);
  await api('/api/revoke',{method:'POST',body:fd});
  await loadShares();
}
loadShares();
</script>
</body>
</html>`))
