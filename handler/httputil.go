package handler

/*
海康开放平台 接口说明文档
https://open.hikvision.com/docs/docId?productId=5c67f1e2f05948198c909700&version=%2F29c78ef52ca842c7933bd2b8e051e9d0&tagPath=%E7%BC%96%E7%A8%8B%E5%BC%95%E5%AF%BC
*/
import (
	"base/log"
	"base/utils"
	"crypto/hmac"
	"crypto/md5"
	"crypto/sha256"
	"crypto/tls"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/google/uuid"
)

func init() {
	appKey := utils.GIniParser.GetStringDefault("hikisc", "ak", "")
	appSecret := utils.GIniParser.GetStringDefault("hikisc", "sk", "")
	ipaddr := utils.GIniParser.GetStringDefault("hikisc", "ip", "")
	port := utils.GIniParser.GetIntDefault("hikisc", "port", 10443)
	isHttps := utils.GIniParser.GetIntDefault("hikisc", "is_https", 1) != 0
	SetPlatformInfo(appKey, appSecret, ipaddr, port, isHttps)
	YsDefaultAppKey = utils.GIniParser.GetStringDefault("ys", "ak", "")
	YsDefaultAppSecret = utils.GIniParser.GetStringDefault("ys", "sk", "")
}

// PlatformConfig 平台配置信息
type PlatformConfig struct {
	AppKey  string // 合作方APPKey
	Secret  string // 合作方APPSecret
	IP      string // 平台IP
	Port    int    // 平台端口，默认HTTPS的443端口
	IsHTTPS bool   // 是否启用HTTPS协议，默认HTTPS
}

var platformConfig *PlatformConfig

// SetPlatformInfo 设置信息参数
func SetPlatformInfo(appkey, secret, ip string, port int, isHttps bool) {
	if port == 0 {
		port = 443
	}

	platformConfig = &PlatformConfig{
		AppKey:  appkey,
		Secret:  secret,
		IP:      ip,
		Port:    port,
		IsHTTPS: isHttps,
	}
}

// HTTPGet HTTP GET请求
// uri: HTTP接口Url，不带协议和端口，如/artemis/api/resource/v1/cameras/indexCode?cameraIndexCode=a10cafaa777c49a5af92c165c95970e0
// timeout: 请求超时时间，单位：秒
func HTTPGet(uri string, timeout int) ([]byte, error) {
	if platformConfig == nil {
		return nil, fmt.Errorf("platform config not initialized")
	}

	headers := make(map[string]string)
	initRequest(headers, uri, "", false)

	// 构建完整URL
	scheme := "http"
	if platformConfig.IsHTTPS {
		scheme = "https"
	}
	fullURL := fmt.Sprintf("%s://%s:%d%s", scheme, platformConfig.IP, platformConfig.Port, uri)

	// 创建HTTP客户端
	client := &http.Client{
		Timeout: time.Duration(timeout) * time.Second,
		Transport: &http.Transport{
			TLSClientConfig: &tls.Config{
				InsecureSkipVerify: true, // 跳过证书验证
			},
		},
	}

	// 创建请求
	req, err := http.NewRequest("GET", fullURL, nil)
	if err != nil {
		return nil, err
	}

	// Accept
	req.Header.Set("Accept", "application/json")
	// Content-Type
	req.Header.Set("Content-Type", "application/json")
	// 设置请求头
	for key, value := range headers {
		if strings.Contains(strings.ToLower(key), "x-ca-") {
			req.Header.Set(key, value)
		}
	}

	// 发送请求
	resp, err := client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	// 处理重定向
	if resp.StatusCode == http.StatusFound || resp.StatusCode == http.StatusMovedPermanently {
		location := resp.Header.Get("Location")
		if location != "" {
			redirectResp, err := client.Get(location)
			if err != nil {
				return nil, err
			}
			defer redirectResp.Body.Close()
			return io.ReadAll(redirectResp.Body)
		}
	}

	if resp.StatusCode == http.StatusOK {
		return io.ReadAll(resp.Body)
	}

	return nil, fmt.Errorf("HTTP request failed with status: %d", resp.StatusCode)
}

// HTTPPost HTTP POST请求
// uri: HTTP接口Url，不带协议和端口，如/artemis/api/resource/v1/org/advance/orgList
// body: 请求参数
// timeout: 请求超时时间，单位：秒
func HTTPPost(uri, body string, timeout int) ([]byte, error) {
	if platformConfig == nil {
		return nil, fmt.Errorf("platform config not initialized")
	}

	headers := make(map[string]string)
	initRequest(headers, uri, body, true)

	// 构建完整URL
	scheme := "http"
	if platformConfig.IsHTTPS {
		scheme = "https"
	}
	fullURL := fmt.Sprintf("%s://%s:%d%s", scheme, platformConfig.IP, platformConfig.Port, uri)

	// 创建HTTP客户端
	client := &http.Client{
		Timeout: time.Duration(timeout) * time.Second,
		Transport: &http.Transport{
			TLSClientConfig: &tls.Config{
				InsecureSkipVerify: true, // 跳过证书验证
			},
		},
	}

	// 创建请求
	req, err := http.NewRequest("POST", fullURL, strings.NewReader(body))
	if err != nil {
		return nil, err
	}

	// Accept
	req.Header.Set("Accept", "application/json")
	// Content-Type
	req.Header.Set("Content-Type", "application/json")
	// 设置请求头
	for key, value := range headers {
		if strings.Contains(strings.ToLower(key), "x-ca-") {
			req.Header.Set(key, value)
		}
	}
	log.Info("POST %s %#v %s", fullURL, req.Header, body)
	//{
	//  Accept: application/json
	//	Content-Type: application/json
	//	x-ca-timestamp: 17485929289300
	//	x-ca-nonce: f9d9b5e1-6272-42c6-816e-47cbbb50a809
	//	x-ca-key: 应用 AppKey（[hikisc] ak）
	//	x-ca-signature-headers: x-ca-key,x-ca-nonce,x-ca-timestamp
	//	x-ca-signature: O9Gi4ZfWTDRoa46ZUbdLxbkGLx5nXjPwL8nqqesDYtc=
	//
	//}

	// 发送请求
	resp, err := client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	// 处理重定向
	if resp.StatusCode == http.StatusFound || resp.StatusCode == http.StatusMovedPermanently {
		location := resp.Header.Get("Location")
		if location != "" {
			redirectResp, err := client.Get(location)
			if err != nil {
				return nil, err
			}
			defer redirectResp.Body.Close()
			return io.ReadAll(redirectResp.Body)
		}
	}

	if resp.StatusCode == http.StatusOK {
		return io.ReadAll(resp.Body)
	}

	return nil, fmt.Errorf("HTTP request failed with status: %d", resp.StatusCode)
}

// initRequest 初始化请求头
func initRequest(headers map[string]string, uri, body string, isPost bool) {
	// Accept
	headers["Accept"] = "application/json"

	// Content-Type
	headers["Content-Type"] = "application/json"

	// content-md5 (仅POST请求)
	if isPost {
		contentMD5 := computeContentMD5(body)
		headers["content-md5"] = contentMD5
	}

	// x-ca-timestamp
	timestamp := strconv.FormatInt(time.Now().UnixMicro()/100, 10)
	println(timestamp)
	headers["x-ca-timestamp"] = "17485956027291"

	// x-ca-nonce
	nonce := uuid.New().String()
	println(nonce)
	headers["x-ca-nonce"] = "91157c54-fad4-47bf-99b4-a559e2dfc9ed"

	// x-ca-key
	headers["x-ca-key"] = platformConfig.AppKey

	// 构建签名字符串
	method := "GET"
	if isPost {
		method = "POST"
	}
	strToSign := buildSignString(method, uri, headers)
	signedStr := computeHMACSHA256(strToSign, platformConfig.Secret)

	// x-ca-signature
	headers["x-ca-signature"] = signedStr
}

// computeContentMD5 计算content-md5
func computeContentMD5(body string) string {
	hash := md5.Sum([]byte(body))
	return base64.StdEncoding.EncodeToString(hash[:])
}

// computeHMACSHA256 计算HMACSHA256
func computeHMACSHA256(str, secret string) string {
	h := hmac.New(sha256.New, []byte(secret))
	h.Write([]byte(str))
	return base64.StdEncoding.EncodeToString(h.Sum(nil))
}

// buildSignString 构建签名字符串
func buildSignString(method, uri string, headers map[string]string) string {
	var sb strings.Builder

	sb.WriteString(strings.ToUpper(method))
	sb.WriteString("\n")

	if accept, ok := headers["Accept"]; ok {
		sb.WriteString(accept)
		sb.WriteString("\n")
	}

	if contentMD5, ok := headers["Content-MD5"]; ok {
		sb.WriteString(contentMD5)
		sb.WriteString("\n")
	}

	if contentType, ok := headers["Content-Type"]; ok {
		sb.WriteString(contentType)
		sb.WriteString("\n")
	}

	if date, ok := headers["Date"]; ok {
		sb.WriteString(date)
		sb.WriteString("\n")
	}

	// 构建签名头
	signHeader := buildSignHeader(headers)
	sb.WriteString(signHeader)
	sb.WriteString(uri)

	return sb.String()
}

// buildSignHeader 构建签名头
func buildSignHeader(headers map[string]string) string {
	var signHeaders []string
	var sb strings.Builder

	// 收集所有x-ca-开头的头部
	for key := range headers {
		if strings.Contains(strings.ReplaceAll(key, " ", ""), "x-ca-") {
			signHeaders = append(signHeaders, key)
		}
	}

	// 按字典序排序
	sort.Strings(signHeaders)

	var headerNames []string
	for _, key := range signHeaders {
		sb.WriteString(key)
		sb.WriteString(":")
		if value, ok := headers[key]; ok && strings.TrimSpace(value) != "" {
			sb.WriteString(value)
		}
		sb.WriteString("\n")
		headerNames = append(headerNames, key)
	}

	// 添加x-ca-signature-headers
	headers["x-ca-signature-headers"] = strings.Join(headerNames, ",")

	return sb.String()
}

type Response struct {
	Code string `json:"code"`
	Msg  string `json:"msg"`
	Data struct {
		PicUrl string `json:"picUrl"`
	} `json:"data"`
}

func Capture(cameraIndexCode string) (string, error) {
	postURI := "/artemis/api/video/v1/manualCapture"
	postBody := fmt.Sprintf(`{"cameraIndexCode": "%s"}`, cameraIndexCode)
	postResponse, err := HTTPPost(postURI, postBody, 30)
	if err != nil {
		return "", err
	}
	var resp Response
	if err := json.Unmarshal(postResponse, &resp); err != nil {
		return "", err
	}

	if resp.Code != "0" {
		return "", errors.New(resp.Msg)
	}

	return resp.Data.PicUrl, nil
}
