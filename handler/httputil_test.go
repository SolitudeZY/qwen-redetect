package handler

import (
	"encoding/json"
	"fmt"
	"testing"
)

func TestCaptureLocal(t *testing.T) {
	t.Skip("旧版本地海康抓图接口已移除，请使用 TestCapture 验证当前 ISC 抓图接口")
}

func TestApi(t *testing.T) {
	// 设置平台信息
	SetPlatformInfo("test-ak", "test-sk", "127.0.0.1", 10443, true)
	// POST 请求示例
	postURI := "/artemis/api/resource/v1/cameras/indexCode"
	postBody := "{\"cameraIndexCode\": \"d1e26cd3152549daa207ce74944fd4e3\"}"
	postResponse, err := HTTPPost(postURI, postBody, 30)
	if err != nil {
		fmt.Printf("POST request failed: %v\n", err)
		return
	}
	fmt.Printf("POST Response: %s\n", string(postResponse))
}

func TestCapture(t *testing.T) {
	SetPlatformInfo("test-ak", "test-sk", "127.0.0.1", 10443, true)
	ret, err := Capture("451bdcca02884e98859820ddda686ada")
	if err != nil {
		t.Fatal(err)
	}
	println(ret)
}

func TestGetCameras(t *testing.T) {
	SetPlatformInfo("test-ak", "test-sk", "127.0.0.1", 10443, true)
	// POST 请求示例
	postURI := "/artemis/api/nms/v1/online/camera/get"
	//postBody := "{\"indexCodes\": [\"451bdcca02884e98859820ddda686ada\"]}"
	postBody := "{\"status\": \"1\"}" // 状态，1-在线，0-离线，-1-未检测
	postResponse, err := HTTPPost(postURI, postBody, 30)
	if err != nil {
		fmt.Printf("POST request failed: %v\n", err)
		return
	}
	fmt.Printf("POST Response: %s\n", string(postResponse))
}

func TestGetCapture(t *testing.T) {
	SetPlatformInfo("test-ak", "test-sk", "127.0.0.1", 10443, true)
	// POST 请求示例
	postURI := "/artemis/api/video/v1/manualCapture"
	//postBody := "{\"cameraIndexCode\": \"228fc267ccbb456ebe1a16936ff19dcc\"}"
	postBody := "{\"cameraIndexCode\": \"5265d4de55e54a2c96c77534178c5c1a\"}"
	postResponse, err := HTTPPost(postURI, postBody, 30)
	if err != nil {
		fmt.Printf("POST request failed: %v\n", err)
		return
	}
	fmt.Printf("POST Response: %s\n", string(postResponse))
}

func TestGetPreviewURLs(t *testing.T) {
	SetPlatformInfo("test-ak", "test-sk", "127.0.0.1", 10443, true)
	// POST 请求示例
	postURI := "/artemis/api/video/v2/cameras/previewURLs"
	postBody := "{\"cameraIndexCode\": \"228fc267ccbb456ebe1a16936ff19dcc\"}"
	postResponse, err := HTTPPost(postURI, postBody, 30)
	if err != nil {
		fmt.Printf("POST request failed: %v\n", err)
		return
	}
	fmt.Printf("POST Response: %s\n", string(postResponse))
}

func TestGetList(t *testing.T) {
	SetPlatformInfo("test-ak", "test-sk", "127.0.0.1", 10443, true)
	// POST 请求示例
	postURI := "/artemis/api/resource/v2/encodeDevice/search"
	postBody := "{\"pageNo\":1, \"pageSize\":1000}"
	postResponse, err := HTTPPost(postURI, postBody, 30)
	if err != nil {
		fmt.Printf("POST request failed: %v\n", err)
		return
	}
	s := string(postResponse)
	println(s)
	var m map[string]interface{}
	_ = json.Unmarshal(postResponse, &m)
	prettyJSON, _ := json.MarshalIndent(m, "", "  ")
	fmt.Printf("POST Response: %s\n", string(prettyJSON))
}

func TestSearch(t *testing.T) {
	SetPlatformInfo("test-ak", "test-sk", "127.0.0.1", 10443, true)
	// POST 请求示例
	postURI := "/artemis/api/resource/v1/cameras"
	postBody := "{\"pageNo\":1, \"pageSize\":1000}"
	postResponse, err := HTTPPost(postURI, postBody, 30)
	if err != nil {
		fmt.Printf("POST request failed: %v\n", err)
		return
	}
	s := string(postResponse)
	println(s)
	var m map[string]interface{}
	_ = json.Unmarshal(postResponse, &m)
	prettyJSON, _ := json.MarshalIndent(m, "", "  ")
	fmt.Printf("POST Response: %s\n", string(prettyJSON))
}
