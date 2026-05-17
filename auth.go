package main

import (
	"crypto"
	"crypto/hmac"
	"crypto/rand"
	"crypto/rsa"
	"crypto/sha1"
	"crypto/sha256"
	"encoding/base32"
	"encoding/base64"
	"encoding/binary"
	"encoding/json"
	"fmt"
	"log"
	"net/http"
	"os"
	"strings"
	"time"
)

// ─── RSA key pair（内存，每次重启重新生成）────────────────────────────────────

var rsaPrivKey *rsa.PrivateKey

func initRSA() {
	key, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		log.Fatal("RSA keygen:", err)
	}
	rsaPrivKey = key
}

// ─── TOTP secret 文件 ────────────────────────────────────────────────────────

const totpSecretPath = "/var/packages/runcmd/var/totp_secret"

func hasSecret() bool {
	_, err := os.Stat(totpSecretPath)
	return err == nil
}

func readSecret() (string, error) {
	data, err := os.ReadFile(totpSecretPath)
	if err != nil {
		return "", err
	}
	return strings.TrimSpace(string(data)), nil
}

// initTOTPGuard 防止初始化窗口期被抢占：
// 若启动时 totp_secret 不存在，10 分钟后仍未配置则自动写入随机密钥。
// 随机密钥只能通过 SSH 登录 NAS 后 cat totpSecretPath 获取，
// 再手动录入验证器 App 完成绑定。
func initTOTPGuard() {
	if hasSecret() {
		return
	}
	log.Printf("[security] TOTP 密钥未配置，请在 10 分钟内完成初始化")
	go func() {
		time.Sleep(10 * time.Minute)
		if hasSecret() {
			return // 用户已在窗口期内完成配置
		}
		raw := make([]byte, 20) // 160-bit 随机熵
		if _, err := rand.Read(raw); err != nil {
			log.Printf("[security] 随机 TOTP 密钥生成失败: %v", err)
			return
		}
		secret := base32.StdEncoding.WithPadding(base32.NoPadding).EncodeToString(raw)
		if err := os.WriteFile(totpSecretPath, []byte(secret+"\n"), 0600); err != nil {
			log.Printf("[security] 随机 TOTP 密钥写入失败: %v", err)
			return
		}
		log.Printf("[security] 初始化超时，已自动生成随机 TOTP 密钥")
		log.Printf("[security] 请 SSH 登录 NAS 执行: cat %s", totpSecretPath)
	}()
}

// ─── TOTP（RFC 4226 HOTP + RFC 6238 TOTP）───────────────────────────────────

func hotp(keyBytes []byte, counter int64) string {
	msg := make([]byte, 8)
	binary.BigEndian.PutUint64(msg, uint64(counter))
	mac := hmac.New(sha1.New, keyBytes)
	mac.Write(msg)
	h := mac.Sum(nil)
	offset := h[len(h)-1] & 0x0f
	code := (int(h[offset]&0x7f)<<24 |
		int(h[offset+1])<<16 |
		int(h[offset+2])<<8 |
		int(h[offset+3])) % 1_000_000
	return fmt.Sprintf("%06d", code)
}

// verifyTOTP 验证当前步长和前一步长（各30s，共60s容错）
func verifyTOTP(secret, code string) bool {
	s := strings.ToUpper(strings.TrimSpace(secret))
	if pad := len(s) % 8; pad != 0 {
		s += strings.Repeat("=", 8-pad)
	}
	keyBytes, err := base32.StdEncoding.DecodeString(s)
	if err != nil {
		return false
	}
	counter := time.Now().Unix() / 30
	return hotp(keyBytes, counter) == code || hotp(keyBytes, counter-1) == code || hotp(keyBytes, counter+1) == code
}

// ─── Token ───────────────────────────────────────────────────────────────────

type tokenClaims struct {
	IssuedAt  int64  `json:"iat"`
	ExpiresAt int64  `json:"exp"`
	Salt      string `json:"salt"`
	Remember  bool   `json:"rem"`
}

const tokenTolerance int64 = 600 // 10 分钟容差（秒）

// makeToken 生成签名 token。remember=true → 365天；false → 1天。
func makeToken(remember bool) (string, error) {
	now := time.Now().Unix()
	var ttl int64
	if remember {
		ttl = 365 * 24 * 3600
	} else {
		ttl = 24 * 3600
	}
	salt := make([]byte, 16)
	if _, err := rand.Read(salt); err != nil {
		return "", err
	}
	claims := tokenClaims{
		IssuedAt:  now,
		ExpiresAt: now + ttl,
		Salt:      base64.RawURLEncoding.EncodeToString(salt),
		Remember:  remember,
	}
	return signClaims(claims)
}

func signClaims(claims tokenClaims) (string, error) {
	payload, _ := json.Marshal(claims)
	b64 := base64.RawURLEncoding.EncodeToString(payload)
	h := sha256.Sum256([]byte(b64))
	sig, err := rsa.SignPKCS1v15(rand.Reader, rsaPrivKey, crypto.SHA256, h[:])
	if err != nil {
		return "", err
	}
	return b64 + "." + base64.RawURLEncoding.EncodeToString(sig), nil
}

// parseToken 验证签名并检查时间有效性，返回 claims。
func parseToken(token string) (*tokenClaims, bool) {
	parts := strings.SplitN(token, ".", 2)
	if len(parts) != 2 {
		return nil, false
	}
	b64, sigB64 := parts[0], parts[1]

	sig, err := base64.RawURLEncoding.DecodeString(sigB64)
	if err != nil {
		return nil, false
	}
	h := sha256.Sum256([]byte(b64))
	if err := rsa.VerifyPKCS1v15(&rsaPrivKey.PublicKey, crypto.SHA256, h[:], sig); err != nil {
		return nil, false
	}
	raw, err := base64.RawURLEncoding.DecodeString(b64)
	if err != nil {
		return nil, false
	}
	var claims tokenClaims
	if err := json.Unmarshal(raw, &claims); err != nil {
		return nil, false
	}
	now := time.Now().Unix()
	if now > claims.ExpiresAt+tokenTolerance || now < claims.IssuedAt-tokenTolerance {
		return nil, false
	}
	return &claims, true
}

// ─── Auth 中间件 ──────────────────────────────────────────────────────────────

// auth 包裹需要鉴权的 handler：验证 X-Token，成功后延签并写入响应头。
func auth(next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		token := r.Header.Get("X-Token")
		if token == "" {
			writeErr(w, 401, "unauthorized")
			return
		}
		claims, ok := parseToken(token)
		if !ok {
			writeErr(w, 401, "invalid or expired token")
			return
		}
		// 每次请求延签，新 token 写入响应头
		if newTok, err := makeToken(claims.Remember); err == nil {
			w.Header().Set("X-Token", newTok)
		}
		next(w, r)
	}
}

// ─── Auth handlers ────────────────────────────────────────────────────────────

// GET /api/auth/status — 无需鉴权
func apiAuthStatus(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		writeErr(w, 405, "method not allowed")
		return
	}
	writeJSON(w, map[string]bool{"has_secret": hasSecret()})
}

// POST /api/auth/login — 无需鉴权，但全局限速（防暴力破解）
func apiAuthLogin(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeErr(w, 405, "method not allowed")
		return
	}
	// 全局无差别限速：所有请求共享同一计数器，2s 内只允许一次尝试
	if !allow("login") {
		writeErr(w, 429, "操作太频繁，请稍后再试")
		return
	}
	var req struct {
		Code     string `json:"code"`
		Remember bool   `json:"remember"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeErr(w, 400, "bad json")
		return
	}
	if len(req.Code) != 6 {
		writeErr(w, 400, "验证码必须为6位数字")
		return
	}
	secret, err := readSecret()
	if err != nil {
		writeErr(w, 500, "未配置 TOTP 密钥")
		return
	}
	if !verifyTOTP(secret, req.Code) {
		writeErr(w, 401, "验证码错误")
		return
	}
	token, err := makeToken(req.Remember)
	if err != nil {
		writeErr(w, 500, "令牌生成失败")
		return
	}
	writeJSON(w, map[string]string{"token": token})
}

// POST /api/auth/secret — 无 secret 时不鉴权；有 secret 时需鉴权 + 旧 TOTP 验证
func apiAuthSecret(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeErr(w, 405, "method not allowed")
		return
	}
	if hasSecret() {
		token := r.Header.Get("X-Token")
		if token == "" {
			writeErr(w, 401, "unauthorized")
			return
		}
		claims, ok := parseToken(token)
		if !ok {
			writeErr(w, 401, "invalid or expired token")
			return
		}
		if newTok, err := makeToken(claims.Remember); err == nil {
			w.Header().Set("X-Token", newTok)
		}
	}

	var req struct {
		Secret string `json:"secret"`
		Code   string `json:"code"` // 更新时需提供当前 TOTP 验证码
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeErr(w, 400, "bad json")
		return
	}
	secret := strings.ToUpper(strings.TrimSpace(req.Secret))
	if secret == "" {
		writeErr(w, 400, "密钥不能为空")
		return
	}
	// 有旧密钥时，必须用当前 TOTP 码二次验证才允许替换
	if hasSecret() {
		if len(req.Code) != 6 {
			writeErr(w, 400, "请提供当前验证器的 6 位验证码")
			return
		}
		oldSecret, err := readSecret()
		if err != nil {
			writeErr(w, 500, "读取密钥失败")
			return
		}
		if !verifyTOTP(oldSecret, req.Code) {
			writeErr(w, 401, "验证码错误，密钥未更新")
			return
		}
	}

	// 校验新密钥是否合法 base32
	padded := secret
	if pad := len(padded) % 8; pad != 0 {
		padded += strings.Repeat("=", 8-pad)
	}
	if _, err := base32.StdEncoding.DecodeString(padded); err != nil {
		writeErr(w, 400, "无效的 Base32 密钥，请检查格式")
		return
	}
	if err := os.WriteFile(totpSecretPath, []byte(secret+"\n"), 0600); err != nil {
		writeErr(w, 500, "保存失败: "+err.Error())
		return
	}
	writeJSON(w, map[string]bool{"ok": true})
}
