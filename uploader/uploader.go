package uploader

import (
	"base/log"
	"base/utils"
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"mime/multipart"
	"net/http"
	"os"
	"path/filepath"
	"sync"
	"time"
)

var GLuanUploader *PhotoUploader

func init() {
	var config = Config{
		BaseURL:  utils.GIniParser.GetStringDefault("luan", "url", "https://jkjc.hebitt.com/ims/api"),
		Username: utils.GIniParser.GetStringDefault("luan", "user", ""),
		Password: utils.GIniParser.GetStringDefault("luan", "pwd", ""),
	}
	GLuanUploader = NewPhotoUploader(config)
}

// Config 配置结构
type Config struct {
	BaseURL  string
	Username string
	Password string
	Timeout  time.Duration
}

// PhotoUploader 图片上传器
type PhotoUploader struct {
	config     Config
	httpClient *http.Client
	token      string
	tokenExp   time.Time
	mutex      sync.RWMutex
}

// NewPhotoUploader 创建新的图片上传器
func NewPhotoUploader(config Config) *PhotoUploader {
	if config.Timeout == 0 {
		config.Timeout = 30 * time.Second
	}

	return &PhotoUploader{
		config: config,
		httpClient: &http.Client{
			Timeout: config.Timeout,
		},
	}
}

// Upload 上传图片（带自动认证）
func (p *PhotoUploader) Upload(fileBytes []byte, ext string, gatewayID string) (string, error) {
	// 生成相对路径
	year := time.Now().Format("2006")
	relativePath := fmt.Sprintf("%s/%s", gatewayID, year)

	// 确保认证有效
	if err := p.ensureAuthenticated(); err != nil {
		return "", fmt.Errorf("认证失败: %v", err)
	}

	// 上传文件
	return p.uploadFileData(fileBytes, ext, relativePath, 1)
}

// ensureAuthenticated 确保认证有效
func (p *PhotoUploader) ensureAuthenticated() error {
	p.mutex.RLock()
	isValid := p.token != "" && time.Now().Before(p.tokenExp)
	p.mutex.RUnlock()

	if isValid {
		return nil
	}

	p.mutex.Lock()
	defer p.mutex.Unlock()

	// 双重检查
	if p.token != "" && time.Now().Before(p.tokenExp) {
		return nil
	}

	return p.authenticate()
}

// authenticate 执行认证
func (p *PhotoUploader) authenticate() error {
	authReq := map[string]string{
		"username": p.config.Username,
		"password": p.config.Password,
	}

	reqBody, err := json.Marshal(authReq)
	if err != nil {
		return err
	}

	req, err := http.NewRequest("POST", p.config.BaseURL+"/ava/authorize", bytes.NewBuffer(reqBody))
	if err != nil {
		return err
	}

	req.Header.Set("Content-Type", "application/json;charset=utf-8")

	resp, err := p.httpClient.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return err
	}

	if resp.StatusCode != 200 {
		return fmt.Errorf("认证失败，状态码: %d, 响应: %s", resp.StatusCode, string(body))
	}

	var authResp struct {
		Status int `json:"status"`
		Result struct {
			Token  string `json:"token"`
			Expire string `json:"expire"`
		} `json:"result"`
	}

	if err := json.Unmarshal(body, &authResp); err != nil {
		return err
	}

	expTime, err := time.Parse(time.RFC3339, authResp.Result.Expire)
	if err != nil {
		log.Warn("parse time failed: %s", authResp.Result.Expire)
		expTime = time.Now().Add(10 * time.Minute)
	}
	p.token = authResp.Result.Token
	p.tokenExp = expTime

	return nil
}

// uploadFileData 上传文件（通过字节内容）
// 参数 fileBytes：文件内容
// 参数 ext：文件扩展名（例如 ".jpg"）
// 参数 relativePath：上传路径
// 参数 no：编号（整数）
func (p *PhotoUploader) uploadFileData(fileBytes []byte, ext string, relativePath string, no int) (string, error) {
	// 构造时间戳文件名
	timestamp := time.Now().Format("20060102150405") // yyyyMMddHHmmss
	if no == 0 {
		no = 1
	}
	fileName := fmt.Sprintf("%s%d%s", timestamp, no, ext)
	retPath := filepath.Join(relativePath, fileName)

	var requestBody bytes.Buffer
	writer := multipart.NewWriter(&requestBody)

	// 创建 form 文件字段
	fileWriter, err := writer.CreateFormFile("file", fileName)
	if err != nil {
		return retPath, err
	}

	// 将内容写入 form
	if _, err := io.Copy(fileWriter, bytes.NewReader(fileBytes)); err != nil {
		return retPath, err
	}

	// 添加额外字段
	if err := writer.WriteField("path", relativePath); err != nil {
		return retPath, err
	}

	if err := writer.Close(); err != nil {
		return retPath, err
	}

	// 创建请求
	req, err := http.NewRequest("POST", p.config.BaseURL+"/ava/upload/photo", &requestBody)
	if err != nil {
		return retPath, err
	}

	req.Header.Set("Content-Type", writer.FormDataContentType())

	// 设置 token
	p.mutex.RLock()
	token := p.token
	p.mutex.RUnlock()
	req.Header.Set("Authorization", token)
	// 发送请求
	resp, err := p.httpClient.Do(req)
	if err != nil {
		return retPath, err
	}
	defer resp.Body.Close()

	if resp.StatusCode == 200 {
		return retPath, nil
	}
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		log.Warn("read upload ret body err: %s", err.Error())
	}
	log.Warn("upload ret: %s", string(body))
	return retPath, fmt.Errorf("上传失败，状态码: %d", resp.StatusCode)
}

// uploadFile 上传文件
func (p *PhotoUploader) uploadFile(filePath, relativePath string) error {
	file, err := os.Open(filePath)
	if err != nil {
		return err
	}
	defer file.Close()

	var requestBody bytes.Buffer
	writer := multipart.NewWriter(&requestBody)

	fileName := filepath.Base(filePath)
	fileWriter, err := writer.CreateFormFile("file", fileName)
	if err != nil {
		return err
	}

	if _, err := io.Copy(fileWriter, file); err != nil {
		return err
	}

	if err := writer.WriteField("path", relativePath); err != nil {
		return err
	}

	if err := writer.Close(); err != nil {
		return err
	}

	req, err := http.NewRequest("POST", p.config.BaseURL+"/upload/photo", &requestBody)
	if err != nil {
		return err
	}

	req.Header.Set("Content-Type", writer.FormDataContentType())

	p.mutex.RLock()
	token := p.token
	p.mutex.RUnlock()

	req.Header.Set("Authorization", "Bearer "+token)

	resp, err := p.httpClient.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()

	if resp.StatusCode == 200 {
		return nil
	}

	return fmt.Errorf("上传失败，状态码: %d", resp.StatusCode)
}
