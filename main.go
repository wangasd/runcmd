package main

import (
	"database/sql"
	"embed"
	"encoding/json"
	"fmt"
	"log"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	_ "modernc.org/sqlite"
)

//go:embed static/index.html static/vue.global.prod.js
var staticFS embed.FS

var db *sql.DB

var (
	ipDir = "/var/packages/runcmd/var/ip"
	ipMu  sync.Mutex
)

var hostnameRE = regexp.MustCompile(`^[A-Za-z0-9](?:[A-Za-z0-9.-]{0,251}[A-Za-z0-9])?$`)

// ─── rate limiter ────────────────────────────────────────────────────────────

var (
	rlMap = map[string]time.Time{}
	rlMu  sync.Mutex
)

const rlDur = 2 * time.Second

func allow(key string) bool {
	rlMu.Lock()
	defer rlMu.Unlock()
	if t, ok := rlMap[key]; ok && time.Since(t) < rlDur {
		return false
	}
	rlMap[key] = time.Now()
	return true
}

// ─── models ──────────────────────────────────────────────────────────────────

type Command struct {
	ID          int64  `json:"id"`
	Name        string `json:"name"`
	CmdTemplate string `json:"cmd_template"`
	List1       string `json:"list1"`
	List2       string `json:"list2"`
	CreatedAt   string `json:"created_at,omitempty"`
}

type RunReq struct {
	Sel1 string `json:"sel1"`
	Sel2 string `json:"sel2"`
}

// ─── db ──────────────────────────────────────────────────────────────────────

func initDB() {
	_, err := db.Exec(`
		CREATE TABLE IF NOT EXISTS commands (
			id           INTEGER  PRIMARY KEY AUTOINCREMENT,
			name         TEXT     NOT NULL,
			cmd_template TEXT     NOT NULL,
			list1        TEXT     NOT NULL DEFAULT '',
			list2        TEXT     NOT NULL DEFAULT '',
			created_at   DATETIME DEFAULT CURRENT_TIMESTAMP
		)`)
	if err != nil {
		log.Fatal("initDB:", err)
	}
}

// ─── helpers ─────────────────────────────────────────────────────────────────

func writeJSON(w http.ResponseWriter, v interface{}) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	_ = json.NewEncoder(w).Encode(v)
}

func writeErr(w http.ResponseWriter, code int, msg string) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(code)
	_ = json.NewEncoder(w).Encode(map[string]string{"error": msg})
}

func cors(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Access-Control-Allow-Origin", "*")
		w.Header().Set("Access-Control-Allow-Methods", "GET,POST,PUT,DELETE,OPTIONS")
		w.Header().Set("Access-Control-Allow-Headers", "Content-Type,X-Token")
		w.Header().Set("Access-Control-Expose-Headers", "X-Token")
		if r.Method == http.MethodOptions {
			w.WriteHeader(http.StatusNoContent)
			return
		}
		next.ServeHTTP(w, r)
	})
}

// ─── handlers ────────────────────────────────────────────────────────────────

// GET /api/commands  → list all
// POST /api/commands → create
func apiCommands(w http.ResponseWriter, r *http.Request) {
	switch r.Method {

	case http.MethodGet:
		rows, err := db.Query(`SELECT id,name,cmd_template,list1,list2,created_at FROM commands ORDER BY id DESC`)
		if err != nil {
			writeErr(w, 500, err.Error())
			return
		}
		defer rows.Close()
		list := []Command{}
		for rows.Next() {
			var c Command
			_ = rows.Scan(&c.ID, &c.Name, &c.CmdTemplate, &c.List1, &c.List2, &c.CreatedAt)
			list = append(list, c)
		}
		writeJSON(w, list)

	case http.MethodPost:
		var c Command
		if err := json.NewDecoder(r.Body).Decode(&c); err != nil {
			writeErr(w, 400, "bad json")
			return
		}
		if strings.TrimSpace(c.Name) == "" || strings.TrimSpace(c.CmdTemplate) == "" {
			writeErr(w, 400, "name and cmd_template required")
			return
		}
		res, err := db.Exec(`INSERT INTO commands(name,cmd_template,list1,list2) VALUES(?,?,?,?)`,
			c.Name, c.CmdTemplate, c.List1, c.List2)
		if err != nil {
			writeErr(w, 500, err.Error())
			return
		}
		c.ID, _ = res.LastInsertId()
		w.WriteHeader(http.StatusCreated)
		writeJSON(w, c)

	default:
		writeErr(w, 405, "method not allowed")
	}
}

// PUT    /api/commands/{id}       → update
// DELETE /api/commands/{id}       → delete  (rate-limited)
// POST   /api/commands/{id}/run   → run     (rate-limited)
func apiCommandByID(w http.ResponseWriter, r *http.Request) {
	seg := strings.TrimPrefix(r.URL.Path, "/api/commands/")
	parts := strings.SplitN(seg, "/", 2)

	id, err := strconv.ParseInt(parts[0], 10, 64)
	if err != nil {
		writeErr(w, 400, "invalid id")
		return
	}

	// /api/commands/{id}/run
	if len(parts) == 2 && parts[1] == "run" {
		if r.Method != http.MethodPost {
			writeErr(w, 405, "method not allowed")
			return
		}
		runCommand(w, r, id)
		return
	}

	switch r.Method {

	case http.MethodPut:
		var c Command
		if err := json.NewDecoder(r.Body).Decode(&c); err != nil {
			writeErr(w, 400, "bad json")
			return
		}
		_, err := db.Exec(`UPDATE commands SET name=?,cmd_template=?,list1=?,list2=? WHERE id=?`,
			c.Name, c.CmdTemplate, c.List1, c.List2, id)
		if err != nil {
			writeErr(w, 500, err.Error())
			return
		}
		c.ID = id
		writeJSON(w, c)

	case http.MethodDelete:
		if !allow(fmt.Sprintf("del:%d", id)) {
			writeErr(w, 429, "操作太频繁，请稍后再试")
			return
		}
		_, err := db.Exec(`DELETE FROM commands WHERE id=?`, id)
		if err != nil {
			writeErr(w, 500, err.Error())
			return
		}
		writeJSON(w, map[string]bool{"ok": true})

	default:
		writeErr(w, 405, "method not allowed")
	}
}

func stripComment(s string) string {
	if i := strings.Index(s, "#"); i >= 0 {
		s = s[:i]
	}
	return strings.TrimSpace(s)
}

func runCommand(w http.ResponseWriter, r *http.Request, id int64) {
	if !allow(fmt.Sprintf("run:%d", id)) {
		writeErr(w, 429, "操作太频繁，请稍后再试")
		return
	}

	var req RunReq
	_ = json.NewDecoder(r.Body).Decode(&req)

	var tmpl string
	if err := db.QueryRow(`SELECT cmd_template FROM commands WHERE id=?`, id).Scan(&tmpl); err != nil {
		writeErr(w, 404, "command not found")
		return
	}

	// 第一组：sel1 按 | 分割，依次替换 {0} {1} {2} …
	// 第二组：sel2 按 | 分割，依次替换 %0 %1 %2 …
	// 每个分段先 strip # 注释再替换，重复占位符全部替换
	assembled := tmpl
	if req.Sel1 != "" {
		for i, v := range strings.Split(req.Sel1, "|") {
			assembled = strings.ReplaceAll(assembled, fmt.Sprintf("{%d}", i), stripComment(v))
		}
	}
	if req.Sel2 != "" {
		for i, v := range strings.Split(req.Sel2, "|") {
			assembled = strings.ReplaceAll(assembled, fmt.Sprintf("%%%d", i), stripComment(v))
		}
	}

	log.Printf("[run] id=%d cmd=%q", id, assembled)

	out, execErr := exec.Command("sh", "-c", assembled).CombinedOutput()

	resp := map[string]interface{}{
		"cmd":    assembled,
		"output": string(out),
		"ok":     execErr == nil,
	}
	if execErr != nil {
		resp["error"] = execErr.Error()
	}
	writeJSON(w, resp)
}

// GET /api/file?file=name.ext → serve a file from var/return/
// 仅允许纯文件名，禁止路径穿越（不含 / 和 ..）
const returnDir = "/var/packages/runcmd/var/return"

func apiFile(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		writeErr(w, 405, "method not allowed")
		return
	}
	name := r.URL.Query().Get("file")
	if name == "" {
		writeErr(w, 400, "missing file parameter")
		return
	}
	// 安全检查：只允许纯文件名，不含任何路径分隔符或特殊序列
	if strings.ContainsAny(name, "/\\") || strings.Contains(name, "..") || name == "." {
		writeErr(w, 400, "invalid file name")
		return
	}
	fullPath := returnDir + "/" + name
	f, err := os.Open(fullPath)
	if err != nil {
		writeErr(w, 404, "file not found")
		return
	}
	defer f.Close()

	stat, err := f.Stat()
	if err != nil || stat.IsDir() {
		writeErr(w, 404, "file not found")
		return
	}

	http.ServeContent(w, r, name, stat.ModTime(), f)
}

// clientIP returns the original client address when the service is behind the
// Synology reverse proxy. Every candidate is parsed before it is used in a file name.
func clientIP(r *http.Request) (string, bool) {
	candidates := []string{}
	if forwarded := r.Header.Get("X-Forwarded-For"); forwarded != "" {
		candidates = append(candidates, strings.Split(forwarded, ",")...)
	}
	candidates = append(candidates, r.Header.Get("X-Real-IP"))
	if host, _, err := net.SplitHostPort(r.RemoteAddr); err == nil {
		candidates = append(candidates, host)
	} else {
		candidates = append(candidates, r.RemoteAddr)
	}
	for _, candidate := range candidates {
		if ip := net.ParseIP(strings.TrimSpace(candidate)); ip != nil {
			return ip.String(), true
		}
	}
	return "", false
}

func validHostname(hostname string) bool {
	if !hostnameRE.MatchString(hostname) || strings.Contains(hostname, "..") {
		return false
	}
	for _, label := range strings.Split(hostname, ".") {
		if label == "" || len(label) > 63 || strings.HasPrefix(label, "-") || strings.HasSuffix(label, "-") {
			return false
		}
	}
	return true
}

func parseLocalIPs(raw string) ([]string, bool) {
	if strings.TrimSpace(raw) == "" {
		return nil, false
	}
	parts := strings.Split(raw, ",")
	if len(parts) > 32 {
		return nil, false
	}
	result := make([]string, 0, len(parts))
	seen := make(map[string]bool, len(parts))
	for _, part := range parts {
		ip := net.ParseIP(strings.TrimSpace(part))
		if ip == nil {
			return nil, false
		}
		normalized := ip.String()
		if !seen[normalized] {
			seen[normalized] = true
			result = append(result, normalized)
		}
	}
	return result, len(result) > 0
}

// GET /api/host/report?hostname=nas01&local_ip=192.168.1.2,10.0.0.2
func apiHostReport(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		writeErr(w, http.StatusMethodNotAllowed, "method not allowed")
		return
	}
	hostname := strings.TrimSpace(r.URL.Query().Get("hostname"))
	if !validHostname(hostname) {
		writeErr(w, http.StatusBadRequest, "invalid hostname")
		return
	}
	rawLocalIP := r.URL.Query().Get("local_ip")
	if rawLocalIP == "" { // compatibility for clients that use localip
		rawLocalIP = r.URL.Query().Get("localip")
	}
	localIPs, ok := parseLocalIPs(rawLocalIP)
	if !ok {
		writeErr(w, http.StatusBadRequest, "invalid local_ip")
		return
	}
	externalIP, ok := clientIP(r)
	if !ok {
		writeErr(w, http.StatusBadRequest, "invalid client ip")
		return
	}
	filename := strings.Join(append([]string{hostname, externalIP}, localIPs...), "-")
	if len(filename) > 240 {
		writeErr(w, http.StatusBadRequest, "host data too long")
		return
	}

	ipMu.Lock()
	defer ipMu.Unlock()
	if err := os.MkdirAll(ipDir, 0750); err != nil {
		writeErr(w, http.StatusInternalServerError, "cannot create ip directory")
		return
	}
	entries, err := os.ReadDir(ipDir)
	if err != nil {
		writeErr(w, http.StatusInternalServerError, "cannot read ip directory")
		return
	}
	now := time.Now()
	for _, entry := range entries {
		if !entry.IsDir() && strings.HasPrefix(entry.Name(), hostname+"-") {
			_ = os.Remove(filepath.Join(ipDir, entry.Name()))
		}
	}
	path := filepath.Join(ipDir, filename)
	file, err := os.OpenFile(path, os.O_CREATE|os.O_WRONLY, 0640)
	if err != nil {
		writeErr(w, http.StatusInternalServerError, "cannot create host file")
		return
	}
	_ = file.Close()
	if err := os.Chtimes(path, now, now); err != nil {
		writeErr(w, http.StatusInternalServerError, "cannot touch host file")
		return
	}
	for _, entry := range entries {
		info, infoErr := entry.Info()
		if infoErr == nil && !entry.IsDir() && now.Sub(info.ModTime()) > 30*24*time.Hour {
			_ = os.Remove(filepath.Join(ipDir, entry.Name()))
		}
	}
	writeJSON(w, map[string]interface{}{"ok": true, "filename": filename})
}

// GET /api/hosts returns file names ordered by modification time, newest first.
func apiHosts(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		writeErr(w, http.StatusMethodNotAllowed, "method not allowed")
		return
	}
	type hostFile struct {
		name    string
		modTime time.Time
	}
	ipMu.Lock()
	defer ipMu.Unlock()
	entries, err := os.ReadDir(ipDir)
	if os.IsNotExist(err) {
		writeJSON(w, []string{})
		return
	}
	if err != nil {
		writeErr(w, http.StatusInternalServerError, "cannot read ip directory")
		return
	}
	files := make([]hostFile, 0, len(entries))
	for _, entry := range entries {
		if entry.IsDir() {
			continue
		}
		info, infoErr := entry.Info()
		if infoErr == nil {
			files = append(files, hostFile{entry.Name(), info.ModTime()})
		}
	}
	sort.SliceStable(files, func(i, j int) bool { return files[i].modTime.After(files[j].modTime) })
	names := make([]string, len(files))
	for i := range files {
		names[i] = files[i].name
	}
	writeJSON(w, names)
}

// GET /api/pubkey → return SSH public key content
func apiPubkey(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		writeErr(w, 405, "method not allowed")
		return
	}
	const pubkeyPath = "/var/packages/runcmd/var/.ssh/id_rsa.pub"
	data, err := os.ReadFile(pubkeyPath)
	if err != nil {
		writeErr(w, 404, "公钥文件不存在，请重新安装套件以生成密钥对")
		return
	}
	writeJSON(w, map[string]string{"pubkey": strings.TrimSpace(string(data))})
}

// ─── main ────────────────────────────────────────────────────────────────────

func main() {
	var err error
	db, err = sql.Open("sqlite", "./runcmd.db")
	if err != nil {
		log.Fatal(err)
	}
	db.SetMaxOpenConns(1) // sqlite: single writer
	defer db.Close()

	initRSA()
	initTOTPGuard()
	initDB()

	mux := http.NewServeMux()
	mux.HandleFunc("/vue-runtime", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet && r.Method != http.MethodHead {
			writeErr(w, http.StatusMethodNotAllowed, "method not allowed")
			return
		}
		data, err := staticFS.ReadFile("static/vue.global.prod.js")
		if err != nil {
			writeErr(w, http.StatusNotFound, "file not found")
			return
		}
		w.Header().Set("Content-Type", "text/javascript; charset=utf-8")
		w.Header().Set("Cache-Control", "public, max-age=31536000, immutable")
		if r.Method == http.MethodGet {
			_, _ = w.Write(data)
		}
	})

	// serve embedded index.html for all non-api routes
	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		data, _ := staticFS.ReadFile("static/index.html")
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		_, _ = w.Write(data)
	})
	// ── Auth（部分无需鉴权）
	mux.HandleFunc("/api/auth/status", apiAuthStatus)
	mux.HandleFunc("/api/auth/login", apiAuthLogin)
	mux.HandleFunc("/api/auth/secret", apiAuthSecret) // 条件鉴权（内部判断）

	// ── 业务接口（均需鉴权）
	mux.HandleFunc("/api/commands", auth(apiCommands))
	mux.HandleFunc("/api/commands/", auth(apiCommandByID))
	mux.HandleFunc("/api/pubkey", auth(apiPubkey))
	// ── 业务接口（无需鉴权）
	mux.HandleFunc("/api/file", apiFile) // 无需鉴权，文件服务,返回json文件模拟api json接口
	mux.HandleFunc("/api/host/report", apiHostReport)
	mux.HandleFunc("/api/hosts", apiHosts)

	addr := "0.0.0.0:38083"
	log.Printf("✓ RunCmd running at http://%s\n", addr)
	log.Fatal(http.ListenAndServe(addr, cors(mux)))
}
