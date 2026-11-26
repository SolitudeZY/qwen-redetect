package handler

import (
	"base/utils"
	"bytes"
	"encoding/json"
	"fmt"
	"io/ioutil"
	"net/http"
	"strings"
	"testing"
	"time"
	"video-recognition/db"
)

func TestRecognize(t *testing.T) {
	//Recognition("FQ6615540", "1", VideoPlatformYingshi)
	time.Sleep(5 * time.Second)
}

func TestUploadImage(t *testing.T) {
	gatewayId := "BMHQZ"
	camera := &db.CameraLLM{GatewayId: &gatewayId, SerialNo: "test"}
	err := UploadImageTest(camera, "https://resources.anxinyun.cn/ai/FQ6615540_20250710061001.jpg")
	if err != nil {
		t.Fatal(err)
	}
}

func TestAiAbnormal(t *testing.T) {
	aiContentRaw, abns, err := utils.AiAli("https://resources.anxinyun.cn/ai/GA8064073_20251124132829.png", AbnormalPrompt, BigModel)
	if err != nil {
		t.Fatal()
		return
	}
	t.Log(aiContentRaw)
	t.Log(abns)
}

func TestSendLocalAlarm(t *testing.T) {
	// 1. 读取本地图片文件
	imgPath := "./test.png"
	imgBytes, err := ioutil.ReadFile(imgPath)
	if err != nil {
		t.Fatal("读取图片失败:", err)
	}

	// 2. 构造消息体
	msg := LocalAlarmMsg{
		CameraId:        "GA8064073",
		AlarmType:       "landslide",
		RecognitionTime: time.Now().UnixMilli(),
		PhotoBytes:      imgBytes,
		Sure:            false,
	}

	// 3. 序列化为 JSON
	body, err := json.Marshal(msg)
	if err != nil {
		t.Fatal("JSON 序列化失败:", err)
	}

	// 4. 执行 HTTP POST
	//url := "https://ai-camera.ngaiot.com/api/alarm"
	url := "http://localhost:8080/api/alarm"

	req, err := http.NewRequest("POST", url, bytes.NewReader(body))
	if err != nil {
		t.Fatal("创建请求失败:", err)
	}
	req.Header.Set("Content-Type", "application/json")

	client := &http.Client{}
	resp, err := client.Do(req)
	if err != nil {
		t.Fatal("请求失败:", err)
	}
	defer resp.Body.Close()

	// 5. 输出响应内容
	respBytes, _ := ioutil.ReadAll(resp.Body)
	t.Log("响应状态码:", resp.StatusCode)
	t.Log("响应内容:", string(respBytes))
}

func TestParseAiResponse(t *testing.T) {
	res := `$$$json
	{
		"warning": false,
		"describe": ""
	}
	$$$

请注意，在实际应用中需要根据具体的监控视频或图片来分析是否有滑坡的现象。如果存在，则应该更新警告状态和描述信息以反映实际情况，并通知相关人员进行进一步的检查或者采取相应的措施应对潜在的风险。
以上仅为一个简单的模板，请结合具体情况调整并完善逻辑流程及代码实现细节等操作步骤来进行更准确地检测工作！`
	res = strings.ReplaceAll(res, "$", "`")
	// 清理并解析 content 中的 JSON 字符串
	aiContent := strings.ReplaceAll(res, "```json", "")
	aiContent = strings.ReplaceAll(aiContent, "```", "")
	aiContent = strings.ReplaceAll(aiContent, "\n", "")
	aiContent = strings.TrimSpace(aiContent)
	aiContent = utils.FindMatchString(aiContent)

	var parsed map[string]interface{}
	if err := json.Unmarshal([]byte(aiContent), &parsed); err != nil {
		t.Fatal("parse err: ", err)
	}
	warning, _ := parsed["warning"].(bool)
	describe, _ := parsed["describe"].(string)
	t.Log("warning:", warning)
	t.Log("describe:", describe)
}

func TestGetFileExtension(t *testing.T) {
	ext := getFileExtension("https://camera.example.com:10443/ngx/proxy?i=PLACEHOLDER")
	println(ext)
}

func TestRefreshSerialNo(t *testing.T) {
	var cameraLLMs []db.CameraLLM
	db.GormDb.Find(&cameraLLMs)
	GetHikCamsV2()
	for _, m := range cameraLLMs {
		info, err := GetHikCamInfo(m.DeviceID)
		if err != nil {
			fmt.Printf("read: %s\n", m.DeviceID)
			hi := GetHikCamFromName(m.Name)
			if hi != nil {
				if m.SerialNo != hi.SerialNo || m.DeviceID != hi.DeviceID {
					m.SerialNo = hi.SerialNo
					m.DeviceID = hi.DeviceID
					if err := db.GormDb.Save(&m).Error; err != nil {
						fmt.Printf("save err: %s %s %s\n", m.DeviceID, m.SerialNo, err.Error())
						continue
					}
					fmt.Printf("updated!!! %s\n", m.SerialNo)
				}
			}
			continue
		}
		if info.SerialNo == m.SerialNo {
			continue
		}
		m.SerialNo = info.SerialNo
		if err := db.GormDb.Save(&m).Error; err != nil {
			fmt.Printf("save err: %s %s %s\n", m.DeviceID, m.SerialNo, err.Error())
			continue
		}
		fmt.Printf("updated!!! %s\n", m.SerialNo)
	}
}

//func TestRecognizeHik(t *testing.T) {
//	RecognitionHikIsc("451bdcca02884e98859820ddda686ada")
//	time.Sleep(5 * time.Second)
//}
