package main

import (
	"database/sql"
	"embed"
	"encoding/json"
	"fmt"
	"log"
	"net/http"
	"os"
	"os/exec"
	"strconv"
	"strings"
	"sync"
	"time"

	_ "modernc.org/sqlite"
)

//go:embed static/index.html
var staticFS embed.FS

var db *sql.DB

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
		w.Header().Set("Access-Control-Allow-Headers", "Content-Type")
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

	// replace placeholders（strip # comments from selections first）
	assembled := strings.ReplaceAll(tmpl, "{0}", stripComment(req.Sel1))
	assembled = strings.ReplaceAll(assembled, "{1}", stripComment(req.Sel2))

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

	initDB()

	mux := http.NewServeMux()

	// serve embedded index.html for all non-api routes
	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		data, _ := staticFS.ReadFile("static/index.html")
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		_, _ = w.Write(data)
	})
	mux.HandleFunc("/api/commands", apiCommands)
	mux.HandleFunc("/api/commands/", apiCommandByID)
	mux.HandleFunc("/api/pubkey", apiPubkey)

	addr := "0.0.0.0:38083"
	log.Printf("✓ RunCmd running at http://%s\n", addr)
	log.Fatal(http.ListenAndServe(addr, cors(mux)))
}
