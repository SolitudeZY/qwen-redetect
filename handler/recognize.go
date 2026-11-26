package handler

import (
	"base/log"
	"base/utils"
	"crypto/tls"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"path"
	"path/filepath"
	"strings"
	"sync"
	"time"
	"video-recognition/db"
	"video-recognition/uploader"
)

var mqttClient *utils.MqttClient
var useQiniu bool
var cameraMutex sync.Mutex
var cameraLastRec map[uint]time.Time
var bigModelIntervalMinute int // 多久用高参数的大模型检查一次
var LocalBaseDirectory string
var LocalImageRetentionDays int
var ImageUrlBase string
var bPushAlarm bool
var BigModel = "qwen-vl-max"
var LiteModel = "qwen2.5-vl-7b-instruct"

const (
	VideoPlatformYingshi = "yingshi"
	VideoPlatformHik     = "hikisc"
)

const (
	AlarmTypeNone      = "none"
	AlarmTypeLandslide = "landslide"
	AlarmTypeCollapse  = "collapse"
	AlarmTypeFire      = "fire"
)

func init() {
	BigModel = utils.GIniParser.GetStringDefault("recognize", "big_model", "qwen-vl-max")
	LiteModel = utils.GIniParser.GetStringDefault("recognize", "lite_model", "qwen3-vl-flash")

	useQiniu = utils.GIniParser.GetIntDefault("recognize", "use_qiniu", 1) != 0
	bigModelIntervalMinute = utils.GIniParser.GetIntDefault("recognize", "big_check_interval_min", 60)
	LocalBaseDirectory = utils.GIniParser.GetStringDefault("recognize", "image_dir", "images")
	LocalImageRetentionDays = utils.GIniParser.GetIntDefault("recognize", "image_retention_days", 60)
	ImageUrlBase = utils.GIniParser.GetStringDefault("recognize", "image_url", "https://ai-camera.ngaiot.com/images")
	mqttUrl := utils.GIniParser.GetStringDefault("mqtt", "url", "tcp://mqtt.datahub.anxinyun.cn:1883")
	mqttClient = utils.NewMqttClient(mqttUrl, "", "", nil)
	bPushAlarm = os.Getenv("PUSH_ALARM") == "true"
	utils.CheckErr(mqttClient.Start())
	cameraLastRec = make(map[uint]time.Time)
	StartCleanupRoutine()
}

func getCameraRecognitionModel(camera *db.CameraLLM) string {
	var bt = time.Now().Add(-time.Duration(bigModelIntervalMinute) * time.Minute)
	cameraMutex.Lock()
	defer cameraMutex.Unlock()
	modelName := LiteModel
	if lt, ok := cameraLastRec[camera.ID]; ok {
		if lt.Before(bt) {
			modelName = BigModel
		}
	} else {
		modelName = BigModel
	}
	cameraLastRec[camera.ID] = time.Now()
	return modelName
}

//func RecognitionCamera(camera *db.CameraLLM) {
//	Recognition(camera.DeviceID, camera.ChannelNo, camera.Type)
//}
//func RecognitionHikIsc(cameraId string) {
//	Recognition(cameraId, "", VideoPlatformHik)
//}

// sure-确定为异常
func Recognition(camera *db.CameraLLM, prompt string, localTriggerMsg *LocalAlarmMsg) (retErr error) {
	var cameraNo = camera.SerialNo
	if cameraNo == "" {
		cameraNo = camera.DeviceID
	}
	aiPrompt := getPrompt(prompt)

	defer func() {
		if r := recover(); r != nil {
			log.Warn("%s 未知异常：%v", cameraNo, r)
			retErr = fmt.Errorf("%s 识别流程异常: %v", cameraNo, r)
		}
	}()
	capTime := time.Now()
	var ext = ".png"
	var err error
	var capturePicUrl, imageUrl, modelName string
	var photoBytes []byte
	if localTriggerMsg == nil || localTriggerMsg.PhotoBytes == nil {
		switch camera.Type {
		case VideoPlatformYingshi:
			capturePicUrl, err = CaptureYs(camera)
		case VideoPlatformHik:
			capturePicUrl, err = Capture(camera.DeviceID)
		default:
			err = fmt.Errorf("unsupported video platform: %s", camera.Type)
		}
		if err != nil {
			return fmt.Errorf("调用抓拍接口失败: %s", err.Error())
		}
		ext = getFileExtension(capturePicUrl)
		log.Info("截图文件地址：%s %s", cameraNo, capturePicUrl)
		photoBytes, imageUrl, err = DownloadImageAndSaveToLocal(capturePicUrl, cameraNo, ext)
		if err != nil {
			return fmt.Errorf("下载截图文件失败! %s", err.Error())
		}
		log.Info("截图保存：%s 图片URL：%s 文件大小:%d", cameraNo, imageUrl, len(photoBytes))
		modelName = getCameraRecognitionModel(camera)
	} else {
		// 通过本地模型触发
		photoBytes = localTriggerMsg.PhotoBytes
		imageUrl, err = saveToLocalFile(photoBytes, cameraNo, ext)
		if err != nil {
			return fmt.Errorf("保存本地触发图片失败: %s", err.Error())
		}
		log.Info("本地模型触发保存：%s 图片URL：%s 文件大小:%d", cameraNo, imageUrl, len(photoBytes))
		modelName = getCameraRecognitionModel(camera)
	}

	rr := &db.Recognition{Cmid: camera.ID, CameraId: cameraNo, ImgPath: imageUrl, Prompt: aiPrompt, Model: &modelName}
	if err = db.GormDb.Create(rr).Error; err != nil {
		return fmt.Errorf("识别记录创建失败: %s %s", cameraNo, err.Error())
	}

	var warning bool
	var describe string
	var aiContentRaw string
	var isTest = strings.Contains(prompt, "#test")
	var isSure = localTriggerMsg != nil && localTriggerMsg.Sure
	recTime := time.Now()
	if !isSure {
		var parsed map[string]interface{}
		if isTest {
			// 测试使用
			aiContentRaw = prompt
			parsed, err = utils.AiParseJson(prompt)
		} else {
			aiContentRaw, parsed, err = utils.AiAli(toDataURL(imageUrl, photoBytes), aiPrompt, modelName)
			log.Info("调用大模型进行识别复核,%s结论: %s err:%v", cameraNo, aiContentRaw, err)
		}
		if err != nil {
			warning = false
			describe = fmt.Sprintf("AI识别出错: %s %s", cameraNo, err.Error())
		} else {
			// 读取 warning 和 describe 字段
			warning, _ = parsed["warning"].(bool)
			describe, _ = parsed["describe"].(string)
		}
	} else {
		warning = true
		describe = "外部识别进程确定。"
	}

	// 主模型告警后复核图像是否完全不可用。复核异常或结果不完整时保留告警，避免漏报。
	if warning && !isTest && !isSure {
		var abns map[string]interface{}
		aiContentRaw, abns, err = utils.AiAli(toDataURL(imageUrl, photoBytes), AbnormalPrompt, modelName)
		if err != nil {
			log.Warn("大模型异常天气、光照等情况复核失败:%s %s", cameraNo, err.Error())
			describe += " 图像有效性复核失败，保留灾害告警并等待人工确认。"
		} else if review, parseErr := parseImageValidityReview(abns); parseErr != nil {
			log.Warn("大模型图像有效性复核结果不完整:%s %s 内容：%s", cameraNo, parseErr.Error(), aiContentRaw)
			describe += " 图像有效性复核结果不完整，保留灾害告警并等待人工确认。"
		} else {
			log.Info("大模型图像有效性复核，图像整体无效:%v 灾害证据可见:%v 原因:%s %s",
				review.ImageInvalid, review.DisasterVisible, review.Reason, cameraNo)
			if review.invalidatesWarning() {
				warning = false
				describe += " 图像主要监测区域因严重环境或画质问题完全不可辨，且未发现清晰灾害证据，认定本次图像无效。"
			}
		}
	}

	rr.Warning = &warning
	rr.Describe = &describe
	rr.RecognitionTime = &recTime
	rr.PhotoBytes = photoBytes

	// 如果有告警
	if localTriggerMsg != nil || (rr.Warning != nil && *rr.Warning == true) {
		if useQiniu {
			filename := fmt.Sprintf("ai/%s_%s%s", cameraNo, capTime.Format("20060102150405"), ext)
			uploadedPicUrl := utils.SavePicBytesTo7n(photoBytes, filename)
			if uploadedPicUrl == "" {
				log.Warn("上传七牛失败: %s", filename)
			} else {
				rr.QiniuImgPath = uploadedPicUrl
				log.Info("%s 上传图片到七牛 %s", cameraNo, uploadedPicUrl)
			}
		}

		if uploader.GLuanUploader == nil {
			log.Warn("%s 上报接口未就绪", cameraNo)
		} else if camera.GatewayId == nil || *camera.GatewayId == "" {
			log.Warn("%s 未指定网关ID", cameraNo)
		} else {
			retpath, err := uploader.GLuanUploader.Upload(rr.PhotoBytes, ext, *camera.GatewayId)
			if err != nil {
				log.Warn("%s 上传到河北路安平台失败：%s", cameraNo, err.Error())
			} else {
				log.Info("%s 上传到河北路安平台：%s", cameraNo, retpath)
				rr.UploadImgPath = retpath
			}
		}
		// 是否需要人工审核
		//if !camera.ManualReview {

		if rr.Warning != nil && *rr.Warning == true {
			if isTest {
				go SendAlarm(rr)
			}
			nt := utils.GIniParser.GetStringDefault("notify", "type", "email")
			if nt == "wx" {
				// 发送企业微信通知
				_ = utils.SendMsgCard(utils.GIniParser.GetStringDefault("wx", "notify", ""), utils.WxMsgCard{
					Title:       "摄像头AI识别通知",
					Description: fmt.Sprintf("AI摄像头%s(%s)识别到异常，请即时查看", camera.Name, rr.CameraId),
					Url:         fmt.Sprintf("https://ai-camera.ngaiot.com/recognition/%d", rr.Id),
					Btntxt:      "查看",
				})
			} else {
				go func() {
					_ = utils.SendMail("摄像头AI识别通知", fmt.Sprintf(`AI摄像头%s(%s)识别到异常，<a href="%s">请即时查看</a>。`,
						camera.Name, rr.CameraId, fmt.Sprintf("https://ai-camera.ngaiot.com/recognition/%d", rr.Id)))
				}()
			}
		} else {
			auditUser := "系统自动判断"
			temp := false
			rr.AuditUser = &auditUser
			rr.Audited = &temp
			rr.AuditTime = &recTime
			go SendEvent(rr)
			log.Info("大模型复核后，判定为正常情况，不进行告警推送")
		}
		//}
	} else {
		// 正常的事件。异常事件需要审核通过后发送
		go SendEvent(rr) // 发布事件，内容更详细. Topic: ai/recognize/{camera_id}
	}

	if err = db.GormDb.Save(rr).Error; err != nil {
		log.Warn("保存识别记录失败: %s %s", cameraNo, err.Error())
	}
	log.Info("[%s] AI判断结果是否告警: %v 描述：%s", cameraNo, warning, describe)
	return nil
}

// toDataURL 将图片字节转为 base64 data URL 传给 DashScope，避免公网 URL 下载失败导致 400
func toDataURL(imageURL string, photoBytes []byte) string {
	if photoBytes == nil {
		b, err := DownloadImage(imageURL)
		if err != nil {
			return imageURL
		}
		photoBytes = b
	}
	mime := "image/png"
	switch strings.ToLower(getFileExtension(imageURL)) {
	case ".jpg", ".jpeg":
		mime = "image/jpeg"
	case ".webp":
		mime = "image/webp"
	case ".bmp":
		mime = "image/bmp"
	}
	return "data:" + mime + ";base64," + base64.StdEncoding.EncodeToString(photoBytes)
}

func getFileExtension(rawURL string) string {
	// 解析 URL
	u, err := url.Parse(rawURL)
	if err != nil {
		return ".png"
	}
	// 获取路径中的文件名部分
	fileName := path.Base(u.Path)
	// 获取后缀名（包括点）
	ext := filepath.Ext(fileName)
	if ext == "" {
		return ".png"
	}
	return ext
}

// 上传路安平台接口测试
func UploadImageTest(camera *db.CameraLLM, capturePicUrl string) error {
	var cameraNo = camera.SerialNo
	if cameraNo == "" {
		cameraNo = camera.DeviceID
	}

	photoBytes, _, err := DownloadImageAndSaveToLocal(capturePicUrl, cameraNo, ".png")
	if err != nil {
		return fmt.Errorf("下载截图文件失败! %s", err.Error())
	}
	ext := path.Ext(capturePicUrl)
	if uploader.GLuanUploader == nil {
		log.Warn("%s 上报接口未就绪", cameraNo)
	} else if camera.GatewayId == nil || *camera.GatewayId == "" {
		log.Warn("%s 未指定网关ID", cameraNo)
	} else {
		retpath, err := uploader.GLuanUploader.Upload(photoBytes, ext, *camera.GatewayId)
		if err != nil {
			log.Warn("%s 上传到河北路安平台失败：%s", cameraNo, err.Error())
		} else {
			log.Info("上传成功: %s", retpath)
		}
	}
	return nil
}

type AlarmMessageRich struct {
	DeviceID   string `json:"deviceId"`
	AlarmTime  int64  `json:"alarmTime"`
	AlarmType  string `json:"alarmType"` // landslide  collapse  fire
	PhotoPath  string `json:"photoPath"`
	PhotoData  string `json:"photoData"`  // Base64图像数据
	AlarmMsg   string `json:"alarmMsg"`   // 告警消息
	DetailMsg  string `json:"detailMsg"`  // 图片识别说明中文解释
	Confidence int    `json:"confidence"` // 置信度
	Level      int    `json:"level"`      // 等级
	Location   struct {
		Lat float64 `json:"lat"`
		Lon float64 `json:"lon"`
	} `json:"location"` // 发生位置
}

type AlarmMessage struct {
	DeviceID  string `json:"deviceId"`
	AlarmTime string `json:"alarmTime"`
	AlarmType string `json:"alarmType"` // landslide  collapse  fire
	PhotoPath string `json:"photoPath"`
}

func toAlarmMessage(rec *db.Recognition) *AlarmMessage {
	return &AlarmMessage{
		DeviceID:  rec.CameraId,
		AlarmTime: rec.AddTime.Format("2006-01-02 15:04:05"),
		AlarmType: AlarmTypeLandslide,
		PhotoPath: rec.UploadImgPath,
	}
}

func toAlarmMessageRich(rec *db.Recognition) AlarmMessageRich {
	alarmType := AlarmTypeNone
	alarmMsg := ""
	if rec.Warning != nil && *rec.Warning == true {
		alarmType = AlarmTypeLandslide
		alarmMsg = fmt.Sprintf("possible %s detected", AlarmTypeLandslide)
	}
	var photoData string
	if rec.PhotoBytes == nil {
		var err error
		if rec.PhotoBytes, err = DownloadImage(rec.ImgPath); err != nil {
			log.Warn("下载图片码流失败: %s %s", rec.ImgPath, err.Error())
		} else {
			photoData = base64.StdEncoding.EncodeToString(rec.PhotoBytes)
		}
	} else {
		photoData = base64.StdEncoding.EncodeToString(rec.PhotoBytes)
	}
	return AlarmMessageRich{
		DeviceID:   rec.CameraId,
		AlarmTime:  rec.AddTime.Unix(),
		AlarmType:  alarmType,
		PhotoPath:  rec.ImgPath,
		AlarmMsg:   alarmMsg,
		PhotoData:  photoData,
		DetailMsg:  safeNilString(rec.Describe),
		Confidence: 100,
		Level:      1,
	}
}

func safeNilString(s *string) string {
	if s == nil {
		return ""
	}
	return *s
}

func DownloadImage(imageURL string) ([]byte, error) {
	u, err := url.Parse(imageURL)
	if err != nil {
		return nil, fmt.Errorf("invalid url: %v", err)
	}

	// 当前服务保存的识别图片优先直接读取本地原文件，避免再经公网回源。
	baseURL, baseErr := url.Parse(ImageUrlBase)
	basePath := ""
	if baseErr == nil {
		basePath = strings.TrimSuffix(path.Clean(baseURL.Path), "/")
	}
	if basePath != "" && (u.Host == "" || u.Host == baseURL.Host) &&
		(u.Path == basePath || strings.HasPrefix(u.Path, basePath+"/")) {
		relativePath := strings.TrimPrefix(strings.TrimPrefix(u.Path, basePath), "/")
		cleanRelativePath := filepath.Clean(filepath.FromSlash(relativePath))
		if cleanRelativePath == "." || cleanRelativePath == ".." || strings.HasPrefix(cleanRelativePath, ".."+string(filepath.Separator)) {
			return nil, fmt.Errorf("invalid local image path")
		}
		localPath := filepath.Join(LocalBaseDirectory, cleanRelativePath)
		log.Info("读取本地文件: %s", localPath)
		imageBytes, localErr := os.ReadFile(localPath)
		if localErr == nil {
			return imageBytes, nil
		}
		if !os.IsNotExist(localErr) {
			return nil, fmt.Errorf("读取本地图片失败: %w", localErr)
		}
		// 本地开发环境可能只同步了数据库，没有同步线上图片目录；继续回源读取。
		log.Info("本地图片不存在，回源读取: %s", imageURL)
	}
	if u.Scheme != "http" && u.Scheme != "https" {
		return nil, fmt.Errorf("unsupported image url scheme: %s", u.Scheme)
	}

	// 创建HTTP客户端
	client := &http.Client{
		Timeout: 30 * time.Second,
		Transport: &http.Transport{
			TLSClientConfig: &tls.Config{
				InsecureSkipVerify: true, // 跳过证书验证
			},
		},
	}
	resp, err := client.Get(imageURL)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode < http.StatusOK || resp.StatusCode >= http.StatusMultipleChoices {
		return nil, fmt.Errorf("download image failed: %s", resp.Status)
	}

	return io.ReadAll(resp.Body)
}

func DownloadImageAndSaveToLocal(imageURL string, cameraNo, ext string) ([]byte, string, error) {
	// 创建HTTP客户端
	client := &http.Client{
		Timeout: 30 * time.Second,
		Transport: &http.Transport{
			TLSClientConfig: &tls.Config{
				InsecureSkipVerify: true, // 跳过证书验证
			},
		},
	}
	resp, err := client.Get(imageURL)
	if err != nil {
		return nil, "", err
	}
	defer resp.Body.Close()
	if resp.StatusCode < http.StatusOK || resp.StatusCode >= http.StatusMultipleChoices {
		return nil, "", fmt.Errorf("download image failed: %s", resp.Status)
	}

	imageBytes, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, "", err
	}

	urlPath, err := saveToLocalFile(imageBytes, cameraNo, ext)
	return imageBytes, urlPath, err
}

func saveToLocalFile(imageBytes []byte, cameraNo, ext string) (string, error) {
	// 获取当前日期和时间
	now := time.Now()
	dateStr := now.Format("2006-01-02") // 日期目录
	timestamp := now.Format("20060102150405")

	// 生成文件名
	if !strings.HasPrefix(ext, ".") {
		ext = "." + ext
	}
	fileName := fmt.Sprintf("%s_%s%s", cameraNo, timestamp, ext)
	dirPath := filepath.Join(LocalBaseDirectory, dateStr)
	filePath := filepath.Join(dirPath, fileName)
	urlPath := fmt.Sprintf("%s/%s/%s", ImageUrlBase, dateStr, fileName)

	// 创建目录
	if err := os.MkdirAll(dirPath, 0755); err != nil {
		return "", err
	}

	// 写入文件
	if err := os.WriteFile(filePath, imageBytes, 0644); err != nil {
		return "", err
	}
	return urlPath, nil
}

// SendAlarm 发布告警，仅当识别到危害 Topic: ai/alarm/{camera_id}
func SendAlarm(rec *db.Recognition) {
	alarmMsg := toAlarmMessage(rec)
	if alarmMsg == nil {
		return
	}
	//if alarmMsg.PhotoPath == "" {
	//	log.Warn("图片未上传，跳过告警发送")
	//	return
	//}
	bts, _ := json.Marshal(alarmMsg)
	payload := string(bts)
	topic := fmt.Sprintf("ai/alarm/%s", rec.CameraId)
	if !bPushAlarm {
		// 测试
		topic = fmt.Sprintf("ai/alarm-test/%s", rec.CameraId)
	}
	err := mqttClient.Publish(topic, payload)
	if err != nil {
		log.Warn("MQTT告警发布失败: %s %s", rec.CameraId, err.Error())
	} else {
		log.Info("MQTT告警发布成功: %s %s", rec.CameraId, payload)
	}
	// 单独保存告警消息
	rr := &db.RecognitionAlert{
		Cmid:            rec.Cmid,
		ImgPath:         rec.ImgPath,
		CameraId:        rec.CameraId,
		Describe:        rec.Describe,
		AddTime:         rec.AddTime,
		RecognitionTime: rec.RecognitionTime,
		AlarmType:       alarmMsg.AlarmType,
		Prompt:          rec.Prompt,
		UploadImgPath:   rec.UploadImgPath,
		Topic:           topic,
		Content:         payload,
		PubRes:          err == nil,
	}
	if err = db.GormDb.Create(rr).Error; err != nil {
		log.Warn("告警记录创建失败: %s %s", rec.CameraId, err.Error())
	}
}

func SendEvent(rec *db.Recognition) {
	payload := toAlarmMessageRich(rec)
	err := mqttClient.Publish(fmt.Sprintf("ai/recognize/%s", rec.CameraId), payload)
	if err != nil {
		log.Warn("MQTT事件发布失败: %s %s", rec.CameraId, err.Error())
	} else {
		payload.PhotoData = ""
		bts, _ := json.Marshal(payload)
		log.Info("MQTT事件发布成功: %s %s", rec.CameraId, string(bts))
	}
}

// StartCleanupRoutine 启动清理协程，定期清理三个月前的目录
func StartCleanupRoutine() {
	go func() {
		time.Sleep(30 * time.Second)
		for {
			files, err := os.ReadDir(LocalBaseDirectory)
			if err != nil {
				log.Warn("清理错误: %v", err)
				time.Sleep(24 * time.Hour)
				continue
			}

			cutoff := time.Now().AddDate(0, 0, -LocalImageRetentionDays)

			for _, file := range files {
				if file.IsDir() {
					dirDate, err := time.Parse("2006-01-02", file.Name())
					if err == nil && dirDate.Before(cutoff) {
						dirPath := filepath.Join(LocalBaseDirectory, file.Name())
						fmt.Println("清理目录:", dirPath)
						_ = os.RemoveAll(dirPath)
					}
				}
			}
			time.Sleep(24 * time.Hour)
		}
	}()
}

func getPrompt(p string) string {
	if strings.TrimSpace(p) == "" {
		return DefaultPrompt
	}
	if !strings.Contains(strings.ToUpper(p), "返回JSON") {
		return p + DefaultOutputPrompt
	}
	return p
}

const DefaultPrompt = `当前图像内容为摄像头监测画面,请对以下自然灾害及工程结构失效场景进行智能识别、分类与异常判断:
识别目标及标准:
1. 倒树检测:识别由于强风、洪水或老化等原因导致的树木倾倒现象.需准确区分正常直立树木与明显倒伏状态(如树干接近水平,或根部抬起).
2. 桥梁垮塌识别:检测桥梁结构整体或局部坍塌事件,包括但不限于桥面断裂、桥墩台沉降、桥体错位、桥板断裂等典型破坏特征.
3. 落梁事件判断:专项识别桥梁梁体脱离支座发生坠落、倾斜移位或悬挂等异常情况.需检测梁体与支座的连接状态异常.
4. 岩体崩塌监测:监测山体边坡区域岩块剥落、滚石、大规模滑坡等现象,需结合地形坡度、岩层结构特征及崩塌物形态,判断是否存在明显地质灾害征兆.

直接输出最终结果，返回结果按照如下格式:
当检测到以上任一异常现象,结果标记 'warning:true',并在 'describe' 字段只输出一句话结论（不超过30字），例如'图像显示桥梁发生局部坍塌'、'图像显示树木倒伏'，不要输出细节.
当未检测到异常,结果标记 'warning:false',并在 'describe' 字段输出一句话说明当前状态正常，例如'当前画面正常，未发现异常现象'.
返回JSON格式示例:
{"warning":true,"describe":""}
该结果应支持 JSON.parse 解析.`

const DefaultOutputPrompt = `

直接输出最终结果，返回结果按照如下格式:
当识别到上述现象时,输出 'warning:true',并在 'describe' 字段只输出一句话概括灾害结论（不超过30字），例如'图像显示大型桥梁发生严重坍塌事故'，不要输出检查过程或细节描述.
返回JSON格式示例:
{"warning":true,"describe":""}
该结果应支持 JSON.parse 解析.`

const AbnormalPrompt = `
你是地质灾害告警系统的图像有效性复核模型。主识别模型已经发现疑似灾害，请独立检查图像是否严重到完全无法判断，并检查明确灾害证据是否仍然可见。

判定规则：
1. image_invalid 只有在大雾、强光过曝、极低照度、暴雨、镜头遮挡或画面损坏覆盖主要监测区域，导致灾害完全无法判断时才为 true。
2. disaster_visible 表示是否能看到滑坡体、新鲜裸露土石、坡体破坏、岩石或泥土堆积、倒伏树木、道路被掩埋等明确灾害证据。
3. 只要明确灾害证据仍然可见，disaster_visible 必须为 true，image_invalid 必须为 false。局部天气或画质问题不得推翻清晰可见的灾害事实。
4. 远处或局部云雾、天空局部过曝、轻微雨雾、局部阴影，以及不影响主要灾害区域识别的情况，image_invalid 必须为 false。
5. 不确定时采用保守原则：不要把图像判为无效，令 image_invalid 为 false，并在 reason 中说明不确定之处。

仅返回一行可被 JSON.parse 解析的 JSON，不得省略字段，不附加任何说明文字：
{"image_invalid":false,"disaster_visible":true,"reason":"简要说明判断依据"}
`
