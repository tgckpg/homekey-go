package app

import (
	"encoding/json"
	"net"
	"net/http"
	"strings"
)

// HA ingress authenticates the user. Only its gateway may reach this listener;
// standalone mode binds to loopback instead.
func Handler(locks []*Lock, readers []ReaderConfig, ingress bool) http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /api/locks", func(w http.ResponseWriter, r *http.Request) {
		statuses := make([]LockStatus, 0, len(locks))
		for _, l := range locks {
			statuses = append(statuses, l.Status())
		}
		readerViews := make([]ReaderView, 0, len(readers))
		for _, reader := range readers {
			v := ReaderView{ReaderConfig: reader}
			if _, err := Group(Assigned(locks, reader.ID)); err != nil {
				v.Warning = err.Error()
			}
			readerViews = append(readerViews, v)
		}
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(struct {
			Locks   []LockStatus `json:"locks"`
			Readers []ReaderView `json:"readers"`
		}{statuses, readerViews})
	})
	mux.HandleFunc("POST /api/locks/{id}/{action}", func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("X-Homekey-Action") != "pairing" {
			http.Error(w, "missing action header", http.StatusForbidden)
			return
		}
		for _, l := range locks {
			if l.Config.ID == r.PathValue("id") {
				switch r.PathValue("action") {
				case "pair":
					if err := l.Pair(); err != nil {
						http.Error(w, err.Error(), http.StatusConflict)
						return
					}
				case "cancel":
					l.CancelPair()
				default:
					http.NotFound(w, r)
					return
				}
				w.WriteHeader(http.StatusNoContent)
				return
			}
		}
		http.NotFound(w, r)
	})
	mux.HandleFunc("GET /{$}", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		w.Write([]byte(page))
	})
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		host, _, err := net.SplitHostPort(r.RemoteAddr)
		if ingress && (err != nil || host != "172.30.32.2") {
			http.Error(w, "ingress only", http.StatusForbidden)
			return
		}
		if origin := r.Header.Get("Origin"); !ingress && origin != "" && !strings.HasSuffix(origin, "://"+r.Host) {
			http.Error(w, "invalid origin", http.StatusForbidden)
			return
		}
		w.Header().Set("Cache-Control", "no-store")
		w.Header().Set("X-Content-Type-Options", "nosniff")
		w.Header().Set("Content-Security-Policy", "default-src 'self'; script-src 'unsafe-inline'; style-src 'unsafe-inline'; connect-src 'self'; frame-ancestors 'self'")
		mux.ServeHTTP(w, r)
	})
}

type ReaderView struct {
	ReaderConfig
	Warning string `json:"warning,omitempty"`
}

const page = `<!doctype html><html><head><meta name="viewport" content="width=device-width,initial-scale=1"><title>Home Key locks</title><style>
body{font:16px system-ui;margin:24px auto;padding:0 20px;max-width:850px;color:#18222b;background:#f4f6f8}article{background:white;border:1px solid #ccd4dc;border-radius:10px;padding:20px;margin:16px 0}h2{margin-top:0}button{padding:10px 18px;margin-right:10px;cursor:pointer}code{font-size:24px}small{color:#52616e}.error{color:#a21d1d}
</style></head><body><h1>Home Key locks</h1><p>Add or edit locks and readers in the app's Configuration tab, then restart. Keep each lock ID unchanged after pairing.</p><p>Pair opens a five-minute HomeKit setup window. Add the lock in Apple Home using the displayed code.</p><p id="error" class="error"></p><main id="locks"></main><h2>Readers</h2><div id="readers"></div><script>
const apiBase=location.pathname+(location.pathname.endsWith("/")?"":"/");
const el=(tag,text)=>{const n=document.createElement(tag);n.textContent=text;return n};
async function action(id,type){try{const r=await fetch(apiBase+'api/locks/'+encodeURIComponent(id)+'/'+type,{method:'POST',headers:{'X-Homekey-Action':'pairing'}});if(!r.ok)throw Error(await r.text());await refresh()}catch(e){document.getElementById('error').textContent=e.message}}
async function refresh(){try{const r=await fetch(apiBase+'api/locks');if(!r.ok)throw Error(await r.text());const data=await r.json();document.getElementById('error').textContent='';const main=document.getElementById('locks');main.replaceChildren();if(!data.locks.length)main.append(el('p','No locks configured. Add a lock in Configuration.'));for(const l of data.locks){const a=el('article','');a.append(el('h2',l.name),el('small','ID: '+l.id+' · HomeKit port: '+l.port),el('p','Readers: '+(l.readers||[]).join(', ')),el('p',l.summary.paired_controllers?'Paired':'Unpaired'));if(l.pin){a.append(el('code',l.pin),el('p','Pairing window ends '+new Date(l.until).toLocaleTimeString()));const b=el('button','Cancel pairing');b.onclick=()=>action(l.id,'cancel');a.append(b)}else if(!l.summary.paired_controllers){const b=el('button','Pair');b.onclick=()=>action(l.id,'pair');a.append(b)}main.append(a)}const readers=document.getElementById('readers');readers.replaceChildren();for(const r of data.readers)readers.append(el('p',r.id+' · '+r.address+' · '+r.adapter+(r.warning?' · '+r.warning:'')));}catch(e){document.getElementById('error').textContent=e.message}}
refresh();setInterval(refresh,2000);
</script></body></html>`
