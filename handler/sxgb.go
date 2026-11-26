package handler

/*
山西省AI摄像头接入平台（sxhis）对接说明：
1、 登录: POST {url}/monitor-push/auth/authLogin，密码需使用平台提供的 SM2(国密2) 公钥加密后传输。
2、 获取国标流播放地址: POST {url}/monitor-push/monitor/str/camera/getDisasterCameraUrlList，
    Header 携带 Authorization: Bearer {access_token}，返回全量 {sip,url} 列表。

SM2密文编码（已用真实抓包请求体核实）：C1C3C2拼接，C1为04未压缩前缀的65字节点坐标（这是
github.com/tjfoc/gmsm的sm2.Encrypt原始输出格式，不需要再手动补04前缀），整体再做标准Base64编码
（不是16进制）。示例密文114字节 = C1(65) + C3(32,SM3摘要) + C2(17,与明文等长)。
*/

import (
	"base/log"
	"base/utils"
	"bytes"
	"crypto/rand"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math/big"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"

	"github.com/tjfoc/gmsm/sm2"
	"video-recognition/db"
)

const (
	VideoPlatformSXGB = "sxgb" // 山西省平台国标流
)

// 平台登录接口 SM2 公钥（未压缩格式：04 + X(32字节) + Y(32字节)）
const sxgbSM2PublicKeyHex = "043f06f8f4c7a64929e10445bc05d1e7b57ac402b6af3fe0bd450d93228db45a7699b3536202c4cbfcc360baf1bca174ac75020ead3346d161bcb9274aed848084"

var (
	sxgbBaseURL     string
	sxgbUsername    string
	sxgbPassword    string
	sxgbIntervalMin int
	sxgbPublicKey   *sm2.PublicKey
)

func init() {
	sxgbBaseURL = strings.TrimSuffix(utils.GIniParser.GetStringDefault("sxgb", "url", "https://www.sxhis.cn"), "/")
	sxgbUsername = utils.GIniParser.GetStringDefault("sxgb", "username", "")
	sxgbPassword = utils.GIniParser.GetStringDefault("sxgb", "password", "")
	sxgbIntervalMin = utils.GIniParser.GetIntDefault("sxgb", "interval_min", 3)
	if sxgbIntervalMin <= 0 {
		sxgbIntervalMin = 3
	}

	var err error
	sxgbPublicKey, err = parseSM2PublicKeyHex(sxgbSM2PublicKeyHex)
	if err != nil {
		log.Warn("[SXGB] 解析国密2公钥失败: %v", err)
	}
}

// parseSM2PublicKeyHex 解析未压缩格式(04+X+Y)的SM2公钥16进制字符串
func parseSM2PublicKeyHex(hexKey string) (*sm2.PublicKey, error) {
	hexKey = strings.TrimPrefix(strings.ToLower(strings.TrimSpace(hexKey)), "04")
	raw, err := hex.DecodeString(hexKey)
	if err != nil {
		return nil, fmt.Errorf("解析16进制失败: %v", err)
	}
	if len(raw) != 64 {
		return nil, fmt.Errorf("公钥长度不正确，期望64字节，实际%d字节", len(raw))
	}
	return &sm2.PublicKey{
		Curve: sm2.P256Sm2(),
		X:     new(big.Int).SetBytes(raw[:32]),
		Y:     new(big.Int).SetBytes(raw[32:]),
	}, nil
}

// sm2EncryptBase64 使用SM2公钥加密，返回C1C3C2拼接密文的标准Base64编码字符串。
// sm2.Encrypt的输出本身已经是 04(未压缩前缀)+X+Y+C3+C2，不需要再手动拼接04前缀。
func sm2EncryptBase64(pub *sm2.PublicKey, plain string) (string, error) {
	if pub == nil {
		return "", fmt.Errorf("sm2公钥未初始化")
	}
	cipher, err := sm2.Encrypt(pub, []byte(plain), rand.Reader, sm2.C1C3C2)
	if err != nil {
		return "", err
	}
	return base64.StdEncoding.EncodeToString(cipher), nil
}

var errSxgbTokenExpired = errors.New("sxgb token expired")

var (
	sxgbTokenMutex   sync.Mutex
	sxgbAccessToken  string
	sxgbTokenExpires time.Time
)

type sxgbLoginResp struct {
	Code int    `json:"code"`
	Msg  string `json:"msg"`
	Data struct {
		AccessToken string `json:"access_token"`
		ExpiresIn   int64  `json:"expires_in"`
	} `json:"data"`
}

// sxgbLoginRaw 调用登录接口，返回 access_token 与有效期（秒）
func sxgbLoginRaw() (string, int64, error) {
	if sxgbUsername == "" || sxgbPassword == "" {
		return "", 0, fmt.Errorf("未配置山西省平台账号密码([sxgb] username/password)")
	}
	encPwd, err := sm2EncryptBase64(sxgbPublicKey, sxgbPassword)
	if err != nil {
		return "", 0, fmt.Errorf("密码加密失败: %v", err)
	}

	body, _ := json.Marshal(map[string]string{
		"username":   sxgbUsername,
		"password":   encPwd,
		"sourceType": "app",
	})

	req, err := http.NewRequest("POST", sxgbBaseURL+"/monitor-push/auth/authLogin", bytes.NewReader(body))
	if err != nil {
		return "", 0, err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Source-Type", "app")

	client := &http.Client{Timeout: 15 * time.Second}
	resp, err := client.Do(req)
	if err != nil {
		return "", 0, err
	}
	defer resp.Body.Close()
	respBytes, err := io.ReadAll(resp.Body)
	if err != nil {
		return "", 0, err
	}

	var result sxgbLoginResp
	if err := json.Unmarshal(respBytes, &result); err != nil {
		return "", 0, fmt.Errorf("解析登录响应失败: %v %s", err, string(respBytes))
	}
	if result.Code != 200 {
		return "", 0, fmt.Errorf("登录失败: code=%d msg=%s", result.Code, result.Msg)
	}
	return result.Data.AccessToken, result.Data.ExpiresIn, nil
}

// getSxgbToken 获取access_token（带缓存，提前5分钟过期）
func getSxgbToken(forceRefresh bool) (string, error) {
	sxgbTokenMutex.Lock()
	defer sxgbTokenMutex.Unlock()

	if !forceRefresh && sxgbAccessToken != "" && time.Now().Before(sxgbTokenExpires) {
		return sxgbAccessToken, nil
	}

	token, expiresIn, err := sxgbLoginRaw()
	if err != nil {
		return "", err
	}
	sxgbAccessToken = token
	expireAfter := time.Duration(expiresIn) * time.Second
	if expireAfter > 5*time.Minute {
		expireAfter -= 5 * time.Minute
	}
	sxgbTokenExpires = time.Now().Add(expireAfter)
	return sxgbAccessToken, nil
}

type sxgbStreamItem struct {
	Url string `json:"url"`
	Sip string `json:"sip"`
}

type sxgbStreamResp struct {
	Code int              `json:"code"`
	Msg  string           `json:"msg"`
	Data []sxgbStreamItem `json:"data"`
}

// fetchSxgbStreamList 调用国标流地址列表接口
func fetchSxgbStreamList(token string) ([]sxgbStreamItem, error) {
	req, err := http.NewRequest("POST", sxgbBaseURL+"/monitor-push/monitor/str/camera/getDisasterCameraUrlList", bytes.NewReader([]byte("{}")))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Source-Type", "app")
	req.Header.Set("Authorization", "Bearer "+token)

	client := &http.Client{Timeout: 15 * time.Second}
	resp, err := client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	respBytes, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, err
	}

	var result sxgbStreamResp
	if err := json.Unmarshal(respBytes, &result); err != nil {
		return nil, fmt.Errorf("解析响应失败: %v %s", err, string(respBytes))
	}
	if result.Code == 401 {
		return nil, errSxgbTokenExpired
	}
	if result.Code != 200 {
		return nil, fmt.Errorf("获取流地址失败: code=%d msg=%s", result.Code, result.Msg)
	}
	return result.Data, nil
}

// getSxgbStreamList 获取流地址列表，token过期时自动重新登录重试一次
func getSxgbStreamList() ([]sxgbStreamItem, error) {
	token, err := getSxgbToken(false)
	if err != nil {
		return nil, err
	}
	list, err := fetchSxgbStreamList(token)
	if errors.Is(err, errSxgbTokenExpired) {
		token, err = getSxgbToken(true)
		if err != nil {
			return nil, err
		}
		return fetchSxgbStreamList(token)
	}
	return list, err
}

// isSxgbStreamUrlExpired 校验播放地址携带的 expired 参数（格式 yyyyMMddHHmmss）是否已过期。
// 未携带该参数或解析失败时，视为未过期（不做额外拦截，交由播放器自身的token校验兜底）。
func isSxgbStreamUrlExpired(rawURL string) bool {
	if rawURL == "" {
		return false
	}
	u, err := url.Parse(rawURL)
	if err != nil {
		return false
	}
	expiredStr := u.Query().Get("expired")
	if expiredStr == "" {
		return false
	}
	expiredAt, err := time.ParseInLocation("20060102150405", expiredStr, time.Local)
	if err != nil {
		log.Warn("[SXGB] 解析播放地址expired参数失败: %s %v", expiredStr, err)
		return false
	}
	return time.Now().After(expiredAt)
}

// RefreshSxgbStreamUrls 刷新所有"山西省平台国标流"摄像头的播放地址，写入 stream_url 列
func RefreshSxgbStreamUrls() {
	defer func() {
		if r := recover(); r != nil {
			log.Warn("[SXGB] 刷新流地址异常: %v", r)
		}
	}()

	if sxgbUsername == "" || sxgbPassword == "" {
		return
	}

	var cameras []db.CameraLLM
	if err := db.GormDb.Where("type = ? AND enabled = ?", VideoPlatformSXGB, true).Find(&cameras).Error; err != nil {
		log.Warn("[SXGB] 查询摄像头配置失败: %v", err)
		return
	}
	if len(cameras) == 0 {
		return
	}

	list, err := getSxgbStreamList()
	if err != nil {
		log.Warn("[SXGB] 获取流地址列表失败: %v", err)
		return
	}
	urlBySip := make(map[string]string, len(list))
	for _, item := range list {
		streamURL := item.Url
		if isSxgbStreamUrlExpired(streamURL) {
			log.Info("[SXGB] SIP:%s 播放地址已过期，忽略: %s", item.Sip, streamURL)
			streamURL = ""
		}
		urlBySip[item.Sip] = streamURL
	}

	updated := 0
	for _, cam := range cameras {
		// 未在返回列表中查到、或地址已过期/为空，都需要清空 stream_url，避免残留失效地址
		streamURL := urlBySip[cam.SerialNo]
		if streamURL == cam.StreamUrl {
			continue
		}
		if err := db.GormDb.Model(&db.CameraLLM{}).Where("id = ?", cam.ID).Update("stream_url", streamURL).Error; err != nil {
			log.Warn("[SXGB] 更新摄像头 %s (SIP:%s) 流地址失败: %v", cam.Name, cam.SerialNo, err)
			continue
		}
		updated++
	}
	log.Info("[SXGB] 流地址刷新完成，共%d个国标流摄像头，更新%d个", len(cameras), updated)
}
