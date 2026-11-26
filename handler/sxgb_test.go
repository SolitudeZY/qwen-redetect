package handler

import (
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/tjfoc/gmsm/sm2"
)

// withSxgbTestConfig 临时替换 sxgb 全局配置（账号、密码、平台地址、token缓存），
// 测试结束后自动还原，避免污染同包内其他测试。
func withSxgbTestConfig(t *testing.T, baseURL, username, password string) {
	t.Helper()
	origBaseURL, origUsername, origPassword := sxgbBaseURL, sxgbUsername, sxgbPassword
	origToken, origExpires := sxgbAccessToken, sxgbTokenExpires

	sxgbBaseURL = baseURL
	sxgbUsername = username
	sxgbPassword = password
	sxgbAccessToken = ""
	sxgbTokenExpires = time.Time{}

	t.Cleanup(func() {
		sxgbBaseURL = origBaseURL
		sxgbUsername = origUsername
		sxgbPassword = origPassword
		sxgbAccessToken = origToken
		sxgbTokenExpires = origExpires
	})
}

func TestIsSxgbStreamUrlExpired(t *testing.T) {
	future := time.Now().Add(1 * time.Hour).Format("20060102150405")
	past := time.Now().Add(-1 * time.Hour).Format("20060102150405")

	cases := []struct {
		name string
		url  string
		want bool
	}{
		{"未过期", "wss://www.sxhis.cn/gb_live/x.flv?token=abc&expired=" + future, false},
		{"已过期", "wss://www.sxhis.cn/gb_live/x.flv?token=abc&expired=" + past, true},
		{"未携带expired参数", "wss://www.sxhis.cn/gb_live/x.flv?token=abc", false},
		{"空地址", "", false},
		{"非法URL", "://bad-url", false},
		{
			"真实案例(已过期地址)",
			"wss://www.sxhis.cn/gb_live/14010500001320000419_14010500001320000419.flv?token=eyJhbGciOiJIUzI1NiIsInR5cCI6IkpXVCJ9.eyJEYXRhIjp7InVzZXJuYW1lIjoiMTQwMTA1MDAwMDEzMjAwMDA0MTlfMTQwMTA1MDAwMDEzMjAwMDA0MTkifSwiaXNzIjoieHhAZ29sYW5nLnNwYWNlIiwiZXhwIjoxNzg0NjA2NDAwLCJpYXQiOjE3ODQ1MjAwMDB9.r0Dm4wJcTYEE-x9YcmhkTt5ey8FEmfUTsDejlQZHqYo&expired=20260721120000",
			true,
		},
	}

	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := isSxgbStreamUrlExpired(c.url); got != c.want {
				t.Fatalf("isSxgbStreamUrlExpired(%q) = %v, want %v", c.url, got, c.want)
			}
		})
	}
}

func TestParseSM2PublicKeyHex(t *testing.T) {
	pub, err := parseSM2PublicKeyHex(sxgbSM2PublicKeyHex)
	if err != nil {
		t.Fatalf("解析接口文档中的国密2公钥失败: %v", err)
	}
	if pub.X == nil || pub.Y == nil {
		t.Fatal("解析出的公钥坐标为空")
	}

	if _, err := parseSM2PublicKeyHex("not-hex-string"); err == nil {
		t.Fatal("非法16进制字符串应返回错误")
	}
	if _, err := parseSM2PublicKeyHex("0400"); err == nil {
		t.Fatal("长度不足的公钥应返回错误")
	}
}

func TestSm2EncryptBase64RoundTrip(t *testing.T) {
	// 用自己生成的密钥对做加解密自洽性验证（文档提供的公钥没有对应私钥，无法在测试中解密）
	priv, err := sm2.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatalf("生成测试密钥对失败: %v", err)
	}

	const plain = "TestPassword_123"
	cipherB64, err := sm2EncryptBase64(&priv.PublicKey, plain)
	if err != nil {
		t.Fatalf("加密失败: %v", err)
	}

	raw, err := base64.StdEncoding.DecodeString(cipherB64)
	if err != nil {
		t.Fatalf("Base64解码失败: %v", err)
	}
	if raw[0] != 0x04 {
		t.Fatalf("密文C1应以04(未压缩点)前缀开头，实际首字节: %02x", raw[0])
	}

	got, err := sm2.Decrypt(priv, raw, sm2.C1C3C2)
	if err != nil {
		t.Fatalf("解密失败: %v", err)
	}
	if string(got) != plain {
		t.Fatalf("解密结果不一致，期望 %q，实际 %q", plain, string(got))
	}

	if _, err := sm2EncryptBase64(nil, plain); err == nil {
		t.Fatal("公钥为空时应返回错误")
	}
}

func TestSxgbLoginRaw_Success(t *testing.T) {
	var gotBody map[string]string
	var gotHeaders http.Header

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/monitor-push/auth/authLogin" {
			t.Errorf("请求路径不正确: %s", r.URL.Path)
		}
		if r.Method != http.MethodPost {
			t.Errorf("请求方法不正确: %s", r.Method)
		}
		gotHeaders = r.Header.Clone()
		_ = json.NewDecoder(r.Body).Decode(&gotBody)

		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"code":200,"msg":null,"data":{"access_token":"test-token-abc","expires_in":144000}}`))
	}))
	defer server.Close()

	withSxgbTestConfig(t, server.URL, "test-user", "test-pass")

	token, expiresIn, err := sxgbLoginRaw()
	if err != nil {
		t.Fatalf("登录失败: %v", err)
	}
	if token != "test-token-abc" {
		t.Fatalf("access_token不符: %s", token)
	}
	if expiresIn != 144000 {
		t.Fatalf("expires_in不符: %d", expiresIn)
	}

	if gotHeaders.Get("Source-Type") != "app" {
		t.Fatalf("Source-Type头不正确: %s", gotHeaders.Get("Source-Type"))
	}
	if !strings.Contains(gotHeaders.Get("Content-Type"), "application/json") {
		t.Fatalf("Content-Type头不正确: %s", gotHeaders.Get("Content-Type"))
	}
	if gotBody["username"] != "test-user" {
		t.Fatalf("username字段不符: %s", gotBody["username"])
	}
	if gotBody["sourceType"] != "app" {
		t.Fatalf("sourceType字段不符: %s", gotBody["sourceType"])
	}
	if gotBody["password"] == "test-pass" {
		t.Fatal("密码不应以明文提交")
	}
	encPwdRaw, err := base64.StdEncoding.DecodeString(gotBody["password"])
	if err != nil {
		t.Fatalf("加密后密码应为合法Base64字符串: %v", err)
	}
	if encPwdRaw[0] != 0x04 {
		t.Fatalf("加密后密码解码后应以04(未压缩点)前缀开头，实际首字节: %02x", encPwdRaw[0])
	}
}

func TestSxgbLoginRaw_ServerError(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"code":500,"msg":"用户不存在/密码错误"}`))
	}))
	defer server.Close()

	withSxgbTestConfig(t, server.URL, "test-user", "test-pass")

	if _, _, err := sxgbLoginRaw(); err == nil {
		t.Fatal("平台返回失败码时应返回错误")
	} else if !strings.Contains(err.Error(), "用户不存在/密码错误") {
		t.Fatalf("错误信息应包含平台返回的msg，实际: %v", err)
	}
}

func TestSxgbLoginRaw_TokenExpiredResponse(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"code":401,"msg":"token 已过期"}`))
	}))
	defer server.Close()

	withSxgbTestConfig(t, server.URL, "test-user", "test-pass")

	if _, _, err := sxgbLoginRaw(); err == nil {
		t.Fatal("code=401时登录也应返回错误")
	}
}

func TestSxgbLoginRaw_MissingCredentials(t *testing.T) {
	withSxgbTestConfig(t, "http://unused.invalid", "", "")

	if _, _, err := sxgbLoginRaw(); err == nil {
		t.Fatal("未配置账号密码时应直接返回错误，不发起请求")
	}
}

func TestGetSxgbToken_CachesUntilExpiryAndForceRefresh(t *testing.T) {
	var loginCalls int
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		loginCalls++
		_, _ = fmt.Fprintf(w, `{"code":200,"msg":null,"data":{"access_token":"token-%d","expires_in":60}}`, loginCalls)
	}))
	defer server.Close()

	withSxgbTestConfig(t, server.URL, "test-user", "test-pass")

	token1, err := getSxgbToken(false)
	if err != nil {
		t.Fatalf("首次获取token失败: %v", err)
	}
	if loginCalls != 1 {
		t.Fatalf("首次获取token应触发一次登录，实际%d次", loginCalls)
	}

	token2, err := getSxgbToken(false)
	if err != nil {
		t.Fatalf("二次获取token失败: %v", err)
	}
	if loginCalls != 1 {
		t.Fatalf("token未过期时不应重复登录，实际登录%d次", loginCalls)
	}
	if token2 != token1 {
		t.Fatalf("未过期时应返回缓存的token，期望%s，实际%s", token1, token2)
	}

	token3, err := getSxgbToken(true)
	if err != nil {
		t.Fatalf("强制刷新token失败: %v", err)
	}
	if loginCalls != 2 {
		t.Fatalf("强制刷新应触发新的登录，实际登录%d次", loginCalls)
	}
	if token3 == token1 {
		t.Fatal("强制刷新后应返回新的token")
	}
}
