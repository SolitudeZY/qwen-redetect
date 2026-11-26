package handler

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"time"
	"video-recognition/db"
)

var (
	YsDefaultAppKey    string
	YsDefaultAppSecret string
)

// TokenResponse 表示获取 access token 返回结构
type TokenResponse struct {
	Code string `json:"code"`
	Data struct {
		AccessToken string `json:"accessToken"`
	} `json:"data"`
}

// CaptureResponse 表示抓拍返回结构
type CaptureResponse struct {
	Code string `json:"code"`
	Data struct {
		PicURL string `json:"picUrl"`
	} `json:"data"`
}

var YsTokenMaps map[string]*YsToken

func init() {
	YsTokenMaps = make(map[string]*YsToken)
}

// getAccessToken 获取 access token（建议缓存）
func getAccessToken(ysAppKey, ysAppSecret string) (string, error) {
	url := "https://open.ys7.com/api/lapp/token/get"
	data := fmt.Sprintf("appKey=%s&appSecret=%s", ysAppKey, ysAppSecret)

	resp, err := http.Post(url, "application/x-www-form-urlencoded", bytes.NewBufferString(data))
	if err != nil {
		return "", fmt.Errorf("请求失败: %v", err)
	}
	defer resp.Body.Close()

	body, _ := io.ReadAll(resp.Body)

	var result TokenResponse
	if err := json.Unmarshal(body, &result); err != nil {
		return "", fmt.Errorf("解析响应失败: %v", err)
	}

	if result.Code == "200" {
		return result.Data.AccessToken, nil
	}
	return "", fmt.Errorf("获取 token 失败，响应: %s", string(body))
}

// captureImage 抓拍图片并返回图片 URL
func CaptureYs(camera *db.CameraLLM) (string, error) {
	deviceSerial := camera.SerialNo
	channelNo := camera.ChannelNo
	var ysk = camera.YingshiSecret
	if ysk == nil {
		ysk = &db.YingshiSecret{
			AppKey:    YsDefaultAppKey,
			AppSecret: YsDefaultAppSecret,
		}
	}

	var token, err = checkYsToken(ysk.AppKey, ysk.AppSecret)
	if err != nil {
		return "", err
	}
	url := "https://open.ys7.com/api/lapp/device/capture"
	data := fmt.Sprintf("accessToken=%s&deviceSerial=%s&channelNo=%s&quality=3",
		token.Token, deviceSerial, channelNo)

	resp, err := http.Post(url, "application/x-www-form-urlencoded", bytes.NewBufferString(data))
	if err != nil {
		return "", fmt.Errorf("请求失败: %v", err)
	}
	defer resp.Body.Close()

	body, _ := io.ReadAll(resp.Body)

	var result CaptureResponse
	if err := json.Unmarshal(body, &result); err != nil {
		return "", fmt.Errorf("解析响应失败: %v", err)
	}

	if result.Code == "200" {
		return result.Data.PicURL, nil
	}
	return "", fmt.Errorf("抓拍失败，响应: %s", string(body))
}

func checkYsToken(appid string, appsecret string) (*YsToken, error) {
	token := YsTokenMaps[appid]
	if token != nil && time.Now().Before(token.NextTokenTime) {
		return token, nil
	}
	ntoken, err := getAccessToken(appid, appsecret)
	if err != nil {
		fmt.Println("获取token失败：", err)
		return nil, err
	}
	nextTokenTime := time.Now().Add(4 * time.Hour)
	YsTokenMaps[appid] = &YsToken{Token: ntoken, NextTokenTime: nextTokenTime}
	return YsTokenMaps[appid], nil
}

type YsToken struct {
	Token         string
	NextTokenTime time.Time
}
