// Package firebase covers Windsurf's Firebase-backed login flow: email +
// password sign-in, ID-token refresh, and re-registration with Codeium. UA
// and Accept-Language are randomised per request — matches the fingerprint
// rotation in src/dashboard/windsurf-login.js.
package firebase

import (
	"crypto/rand"
	"encoding/json"
	"fmt"
	"math/big"
	"strconv"

	"windsurfapi/internal/cloud"
	"windsurfapi/internal/langserver"
	"windsurfapi/internal/logx"
)

// Constant API key extracted from Windsurf's web bundle. CLAUDE.md notes
// three other keys were tried and do NOT work — don't rotate.
const FirebaseAPIKey = "AIzaSyDsOl-1XpT5err0Tcnx8FFod1H8gVGIycY"

const (
	authURL    = "https://identitytoolkit.googleapis.com/v1/accounts:signInWithPassword?key=" + FirebaseAPIKey
	refreshURL = "https://securetoken.googleapis.com/v1/token?key=" + FirebaseAPIKey
)

var (
	osVersions = []string{
		"Windows NT 10.0; Win64; x64",
		"Windows NT 10.0; WOW64",
		"Macintosh; Intel Mac OS X 10_15_7",
		"Macintosh; Intel Mac OS X 11_6_0",
		"Macintosh; Intel Mac OS X 12_3_1",
		"Macintosh; Intel Mac OS X 13_4_1",
		"Macintosh; Intel Mac OS X 14_2_1",
		"X11; Linux x86_64",
		"X11; Ubuntu; Linux x86_64",
	}
	chromeVersions = []string{
		"120.0.0.0", "121.0.0.0", "122.0.0.0", "123.0.0.0", "124.0.0.0",
		"125.0.0.0", "126.0.0.0", "127.0.0.0", "128.0.0.0", "129.0.0.0",
		"130.0.0.0", "131.0.0.0", "132.0.0.0", "133.0.0.0", "134.0.0.0",
	}
	acceptLanguages = []string{
		"en-US,en;q=0.9", "en-GB,en;q=0.9", "zh-TW,zh;q=0.9,en;q=0.8",
		"zh-CN,zh;q=0.9,en;q=0.8", "ja,en-US;q=0.9,en;q=0.8",
		"ko,en-US;q=0.9,en;q=0.8", "de,en-US;q=0.9,en;q=0.8",
		"fr,en-US;q=0.9,en;q=0.8", "es,en-US;q=0.9,en;q=0.8",
		"pt-BR,pt;q=0.9,en;q=0.8",
	}
)

// pick draws one element uniformly via crypto/rand. Non-crypto would be
// sufficient for a UA-fingerprint shuffle, but using the crypto source
// costs us nothing and avoids a separate math/rand surface.
func pick(arr []string) string {
	if len(arr) == 0 {
		return ""
	}
	n, err := rand.Int(rand.Reader, big.NewInt(int64(len(arr))))
	if err != nil {
		return arr[0]
	}
	return arr[n.Int64()]
}

func fingerprint() map[string]string {
	os := pick(osVersions)
	ver := pick(chromeVersions)
	major := ver
	for i := 0; i < len(ver); i++ {
		if ver[i] == '.' {
			major = ver[:i]
			break
		}
	}
	ua := fmt.Sprintf("Mozilla/5.0 (%s) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/%s Safari/537.36", os, ver)
	platform := `"Linux"`
	switch {
	case containsFold(os, "Windows"):
		platform = `"Windows"`
	case containsFold(os, "Mac"):
		platform = `"macOS"`
	}
	return map[string]string{
		"User-Agent":         ua,
		"Accept-Language":    pick(acceptLanguages),
		"Accept":             "application/json, text/plain, */*",
		"Accept-Encoding":    "identity",
		"sec-ch-ua":          fmt.Sprintf(`"Chromium";v="%s", "Google Chrome";v="%s", "Not-A.Brand";v="99"`, major, major),
		"sec-ch-ua-mobile":   "?0",
		"sec-ch-ua-platform": platform,
		"Sec-Fetch-Dest":     "empty",
		"Sec-Fetch-Mode":     "cors",
		"Sec-Fetch-Site":     "cross-site",
		"Origin":             "https://windsurf.com",
		"Referer":            "https://windsurf.com/",
	}
}

func containsFold(s, sub string) bool {
	if len(s) < len(sub) {
		return false
	}
	for i := 0; i+len(sub) <= len(s); i++ {
		ok := true
		for j := 0; j < len(sub); j++ {
			c1 := s[i+j]
			c2 := sub[j]
			if 'A' <= c1 && c1 <= 'Z' {
				c1 += 'a' - 'A'
			}
			if 'A' <= c2 && c2 <= 'Z' {
				c2 += 'a' - 'A'
			}
			if c1 != c2 {
				ok = false
				break
			}
		}
		if ok {
			return true
		}
	}
	return false
}

// ─── Login ────────────────────────────────────────────────

// LoginError exposes the Firebase error code so the dashboard can present a
// user-friendly hint. IsAuthFail flips on only for bad-credential cases so
// the dashboard doesn't offer a "try again" retry on permanent failures.
type LoginError struct {
	Code       string
	Friendly   string
	IsAuthFail bool
}

func (e *LoginError) Error() string { return "Firebase 登入失败: " + e.Friendly }

var friendlyMap = map[string]struct {
	Msg        string
	IsAuthFail bool
}{
	"EMAIL_NOT_FOUND":          {"该邮箱未注册邮箱密码登录方式（可能用 Google/GitHub 注册）", true},
	"INVALID_PASSWORD":         {"密码错误（若用 Google/GitHub 登录请改用 OAuth 或 Auth Token）", true},
	"INVALID_LOGIN_CREDENTIALS": {"邮箱或密码错误", true},
	"USER_DISABLED":            {"账号已被停用", false},
	"TOO_MANY_ATTEMPTS_TRY_LATER": {"尝试太多次 请稍后再试", false},
	"INVALID_EMAIL":            {"邮箱格式错误", true},
}

// LoginResult bundles everything Windsurf-login emits.
type LoginResult struct {
	APIKey       string
	Name         string
	Email        string
	IDToken      string
	RefreshToken string
	APIServerURL string
}

// Login runs the full Windsurf sign-in: Firebase → register_user.
func Login(email, password string, proxy *langserver.Proxy) (LoginResult, error) {
	fp := fingerprint()
	logx.Info("Windsurf login: %s proxy=%s", email, proxyLabel(proxy))

	fbBody, _ := json.Marshal(map[string]any{
		"email": email, "password": password, "returnSecureToken": true,
	})

	headers := map[string]string{"Content-Type": "application/json"}
	for k, v := range fp {
		headers[k] = v
	}

	status, raw, err := cloud.PostJSON(authURL, fbBody, proxy, headers)
	if err != nil {
		return LoginResult{}, err
	}
	var fbResp struct {
		IDToken      string `json:"idToken"`
		RefreshToken string `json:"refreshToken"`
		LocalID      string `json:"localId"`
		Error        *struct {
			Message string `json:"message"`
		} `json:"error"`
	}
	if err := json.Unmarshal(raw, &fbResp); err != nil {
		return LoginResult{}, fmt.Errorf("firebase parse (%d): %s", status, string(raw[:min(200, len(raw))]))
	}
	if fbResp.Error != nil {
		code := fbResp.Error.Message
		e := &LoginError{Code: code, Friendly: code}
		if f, ok := friendlyMap[code]; ok {
			e.Friendly = f.Msg
			e.IsAuthFail = f.IsAuthFail
		}
		return LoginResult{}, e
	}
	if fbResp.IDToken == "" {
		return LoginResult{}, fmt.Errorf("firebase response missing idToken")
	}
	logx.Info("Firebase login OK: %s UID=%s", email, fbResp.LocalID)

	reg, err := cloud.RegisterUser(fbResp.IDToken, proxy)
	if err != nil {
		return LoginResult{}, err
	}
	name := reg.Name
	if name == "" {
		name = email
	}
	logx.Info("Codeium register OK: %s → key=%s...", email, safePrefix(reg.APIKey, 12))
	return LoginResult{
		APIKey: reg.APIKey, Name: name, Email: email,
		IDToken: fbResp.IDToken, RefreshToken: fbResp.RefreshToken,
		APIServerURL: reg.APIServerURL,
	}, nil
}

// RefreshToken exchanges a refresh token for a fresh ID token.
type RefreshedTokens struct {
	IDToken      string
	RefreshToken string
	ExpiresIn    int
}

func RefreshToken(refreshToken string, proxy *langserver.Proxy) (RefreshedTokens, error) {
	if refreshToken == "" {
		return RefreshedTokens{}, fmt.Errorf("no refresh token")
	}
	body := []byte("grant_type=refresh_token&refresh_token=" + urlQueryEscape(refreshToken))
	headers := map[string]string{
		"Content-Type": "application/x-www-form-urlencoded",
		"Referer":      "https://windsurf.com/",
		"Origin":       "https://windsurf.com",
		"User-Agent":   "Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36 Chrome/130.0.0.0 Safari/537.36",
	}
	status, raw, err := cloud.PostJSON(refreshURL, body, proxy, headers)
	if err != nil {
		return RefreshedTokens{}, err
	}
	if status >= 400 {
		return RefreshedTokens{}, fmt.Errorf("firebase refresh %d: %s", status, snippet(raw, 200))
	}
	var parsed struct {
		IDToken      string `json:"id_token"`
		AltIDToken   string `json:"idToken"`
		RefreshToken string `json:"refresh_token"`
		AltRefresh   string `json:"refreshToken"`
		ExpiresIn    string `json:"expires_in"`
		AltExpires   string `json:"expiresIn"`
	}
	if err := json.Unmarshal(raw, &parsed); err != nil {
		return RefreshedTokens{}, err
	}
	idTok := parsed.IDToken
	if idTok == "" {
		idTok = parsed.AltIDToken
	}
	if idTok == "" {
		return RefreshedTokens{}, fmt.Errorf("firebase refresh: no idToken in response")
	}
	rt := parsed.RefreshToken
	if rt == "" {
		rt = parsed.AltRefresh
	}
	if rt == "" {
		rt = refreshToken
	}
	exp := parseIntOr(parsed.ExpiresIn, parseIntOr(parsed.AltExpires, 3600))
	logx.Info("Firebase token refreshed, expires in %ds", exp)
	return RefreshedTokens{IDToken: idTok, RefreshToken: rt, ExpiresIn: exp}, nil
}

// ReRegister uses a refreshed id token to re-register and get a fresh api key.
func ReRegister(idToken string, proxy *langserver.Proxy) (cloud.RegisterResult, error) {
	return cloud.RegisterUser(idToken, proxy)
}

// ─── helpers ──────────────────────────────────────────────

func proxyLabel(p *langserver.Proxy) string {
	if p == nil || p.Host == "" {
		return "none"
	}
	return fmt.Sprintf("%s:%d", p.Host, p.Port)
}
func safePrefix(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n]
}
func snippet(b []byte, n int) string {
	if len(b) <= n {
		return string(b)
	}
	return string(b[:n])
}
func min(a, b int) int {
	if a < b {
		return a
	}
	return b
}
func urlQueryEscape(s string) string {
	const hex = "0123456789ABCDEF"
	out := make([]byte, 0, len(s))
	for i := 0; i < len(s); i++ {
		c := s[i]
		if 'a' <= c && c <= 'z' || 'A' <= c && c <= 'Z' || '0' <= c && c <= '9' || c == '-' || c == '_' || c == '.' || c == '~' {
			out = append(out, c)
			continue
		}
		out = append(out, '%', hex[c>>4], hex[c&0x0F])
	}
	return string(out)
}
func parseIntOr(s string, def int) int {
	if s == "" {
		return def
	}
	n, err := strconv.Atoi(s)
	if err != nil {
		return def
	}
	return n
}
