package handler

import (
	"base/log"
	"encoding/json"
	"fmt"
	"os"
)

// HikCamInfo 海康摄像头信息
type HikCamInfo struct {
	DeviceID  string `json:"device_id"`
	SerialNo  string `json:"serial_no"`
	Name      string `json:"name"`
	Model     string `json:"model"`
	ChannelNo string `json:"channel_no"`
}

func (h HikCamInfo) String() string {
	return h.Name
	//return fmt.Sprintf("%s(%s)", h.Name, h.DeviceID)
}

var hikCams = make(map[string]HikCamInfo)

func GetHikCamInfo(serialNo string) (*HikCamInfo, error) {
	if info, exists := hikCams[serialNo]; exists {
		return &info, nil
	}
	return nil, fmt.Errorf("摄像头信息未找到")
}

func GetHikCamFromName(name string) *HikCamInfo {
	for _, info := range hikCams {
		if info.Name == name {
			return &info
		}
	}
	return nil
}

// CameraResponse 定义返回结构体
type CameraResponse struct {
	Code string `json:"code"`
	Msg  string `json:"msg"`
	Data struct {
		List     []CameraInfo `json:"list"`
		PageNo   int          `json:"pageNo"`
		PageSize int          `json:"pageSize"`
		Total    int          `json:"total"`
	} `json:"data"`
}

type CameraInfo struct {
	CameraIndexCode    string   `json:"cameraIndexCode"`
	EncodeDevIndexCode string   `json:"encodeDevIndexCode"`
	CameraName         string   `json:"cameraName"`
	CameraType         int      `json:"cameraType"`
	UpdateTime         string   `json:"updateTime"`
	Latitude           *float64 `json:"latitude"`
	Longitude          *float64 `json:"longitude"`
}

// CameraResponse 定义返回结构体
type CameraResponseV2 struct {
	Code string `json:"code"`
	Msg  string `json:"msg"`
	Data struct {
		List     []CameraInfoV2 `json:"list"`
		PageNo   int            `json:"pageNo"`
		PageSize int            `json:"pageSize"`
		Total    int            `json:"total"`
	} `json:"data"`
}

type CameraInfoV2 struct {
	CameraIndexCode string   `json:"indexCode"`
	SerialNo        string   `json:"deviceCode"`
	CameraName      string   `json:"name"`
	CameraType      *string  `json:"deviceType"`
	UpdateTime      string   `json:"updateTime"`
	Latitude        *float64 `json:"latitude"`
	Longitude       *float64 `json:"longitude"`
}

func GetHikCamsV2() []HikCamInfo {
	if os.Getenv("HIK_ENABLE") == "false" {
		return []HikCamInfo{}
	}
	resultDvs := getHicDevices()
	if resultDvs == nil {
		log.Warn("接口返回空")
		return nil
	}
	if resultDvs.Code != "0" {
		log.Warn("接口返回错误：%s", resultDvs.Msg)
		return nil
	}
	var temp = make(map[string]HikCamInfo)
	for _, cam := range resultDvs.Data.List {
		hikCam := HikCamInfo{
			Name:      cam.CameraName,
			DeviceID:  cam.CameraIndexCode,
			SerialNo:  cam.SerialNo,
			ChannelNo: "1",
		}
		temp[cam.CameraIndexCode] = hikCam
	}

	var result = getHicCameras()
	if result.Code != "0" {
		log.Warn("接口返回错误：%s", result.Msg)
		return nil
	}
	var hiks []HikCamInfo
	for _, cam := range result.Data.List {
		var sn = ""
		if tm, found := temp[cam.EncodeDevIndexCode]; found {
			sn = tm.SerialNo
		}
		hikCam := HikCamInfo{
			Name:      cam.CameraName,
			DeviceID:  cam.CameraIndexCode,
			SerialNo:  sn,
			ChannelNo: "1",
		}
		hikCams[sn] = hikCam
		hiks = append(hiks, hikCam)
	}
	return hiks
}

func getHicDevices() *CameraResponseV2 {
	postURI := "/artemis/api/resource/v2/encodeDevice/search"
	postBody := `{"pageNo":1, "pageSize":1000}`

	respBytes, err := HTTPPost(postURI, postBody, 30)
	if err != nil {
		log.Warn("POST request failed: %v", err)
		return nil
	}
	var resultDvs CameraResponseV2
	if err := json.Unmarshal(respBytes, &resultDvs); err != nil {
		log.Warn("解析JSON失败: %v", err)
		return nil
	}
	return &resultDvs
}

func getHicCameras() *CameraResponse {
	postURI := "/artemis/api/resource/v1/cameras"
	postBody := `{"pageNo":1, "pageSize":1000}`

	respBytes, err := HTTPPost(postURI, postBody, 30)
	if err != nil {
		log.Warn("POST request failed: %v", err)
		return nil
	}
	var result CameraResponse
	if err := json.Unmarshal(respBytes, &result); err != nil {
		log.Warn("解析JSON失败: %v", err)
		return nil
	}
	return &result
}
