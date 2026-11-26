package handler

import (
	"base/log"
	"base/utils"
	"bytes"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"github.com/gin-gonic/gin"
	"html/template"
	"io"
	"net/http"
	"strconv"
	"strings"
	"time"
	"video-recognition/db"
)

var htmlTemplate = "index.html"
var htmlHistoryTemplate = "history.html"
var htmlRecognitionsTemplate = "recognitions.html"
var htmlImageDetectionTemplate = "image_detection.html"
var recognizeBeginTime time.Time
var imageDetectionURL string

const maxDetectionImageSize = 20 << 20

func init() {
	recognizeBeginTime, _ = time.Parse("2006-01-02", "2025-12-01")
	imageDetectionURL = strings.TrimSpace(utils.GIniParser.GetStringDefault("detect", "url", "http://127.0.0.1:9090/detect"))
}

// 处理程序
func indexHandler(c *gin.Context) {
	var cameraLLMs []db.CameraLLM
	db.GormDb.Order("id").Find(&cameraLLMs)

	var groups []string
	db.GormDb.Model(&db.CameraLLM{}).
		Distinct("group").
		Where("\"group\" != ?", "").
		Order("\"group\"").
		Pluck("group", &groups)

	HikCams := GetHikCamsV2()
	if HikCams == nil {
		c.JSON(500, gin.H{"error": "海康摄像头列表加载失败"})
		return
	}

	tmpl, err := template.ParseFiles(htmlTemplate)
	if err != nil {
		c.JSON(500, gin.H{"error": "模板解析错误"})
		return
	}

	data := struct {
		CameraLLMs []db.CameraLLM
		HikCams    []HikCamInfo
		Groups     []string
	}{
		CameraLLMs: cameraLLMs,
		HikCams:    HikCams,
		Groups:     groups,
	}

	c.Header("Content-Type", "text/html; charset=utf-8")
	_ = tmpl.Execute(c.Writer, data)
}

func createCameraLLMHandler(c *gin.Context) {
	var camera db.CameraLLM

	camera.Type = c.PostForm("type")
	camera.Name = c.PostForm("name")
	camera.CustomPrompt = c.PostForm("custom_prompt")
	camera.LLMModelType = c.PostForm("llm_model_type")
	camera.Group = c.PostForm("group")
	gateway_id := c.PostForm("gateway_id")
	camera.GatewayId = &gateway_id
	camera.Enabled = c.PostForm("enabled") == "true"
	camera.ManualReview = c.PostForm("manual_review") == "true"

	if gateway_id == "" && camera.Type != VideoPlatformSXGB {
		c.JSON(500, gin.H{"error": "接入网关ID不能为空"})
		return
	}
	intervalStr := c.PostForm("interval_sec")
	if interval, err := strconv.Atoi(intervalStr); err == nil {
		camera.IntervalSec = interval
	}

	if camera.Type == "yingshi" {
		camera.SerialNo = c.PostForm("serial_no")
		camera.DeviceID = camera.SerialNo
		camera.DeviceName = camera.Name
		camera.ChannelNo = "1"
		camera.Model = "萤石摄像头" // 萤石设备型号需要通过API获取
	} else if camera.Type == "hikisc" {
		hikSerialNo := c.PostForm("hik_serial_no")
		if hikSerialNo != "" {
			// 获取海康摄像头详细信息
			if info, err := GetHikCamInfo(hikSerialNo); err == nil {
				camera.SerialNo = info.SerialNo
				camera.DeviceID = info.DeviceID
				camera.DeviceName = info.Name
				camera.ChannelNo = info.ChannelNo
				camera.Model = info.Model
			}
		}
	} else if camera.Type == VideoPlatformSXGB {
		camera.SerialNo = c.PostForm("sxgb_serial_no") // SIP编码
		camera.DeviceID = camera.SerialNo
		camera.DeviceName = camera.Name
		camera.ChannelNo = "1"
		camera.Model = "山西省平台国标流"
	}

	if err := db.GormDb.Create(&camera).Error; err != nil {
		c.JSON(500, gin.H{"error": "创建配置失败: " + err.Error()})
		return
	}

	// 创建定时任务
	if err := GTaskManager.AddTask(&camera); err != nil {
		log.Warn("创建定时任务失败: %v", err)
		// 注意：这里不返回错误，因为配置已经创建成功
	}
	c.Redirect(302, "/")
}

func updateCameraLLMHandler(c *gin.Context) {
	id := c.Param("id")
	var camera db.CameraLLM

	if err := db.GormDb.First(&camera, id).Error; err != nil {
		c.JSON(404, gin.H{"error": "配置未找到"})
		return
	}

	oldEnabled := camera.Enabled
	oldInterval := camera.IntervalSec

	var updateData struct {
		Type         string `json:"type"`
		Name         string `json:"name"`
		SerialNo     string `json:"serial_no"`
		Group        string `json:"group"`
		GatewayId    string `json:"gateway_id"`
		IntervalSec  int    `json:"interval_sec,string"`
		LLMModelType string `json:"llm_model_type"`
		CustomPrompt string `json:"custom_prompt"`
		Enabled      bool   `json:"enabled"`
		ManualReview bool   `json:"manual_review"`
	}

	if err := c.ShouldBindJSON(&updateData); err != nil {
		c.JSON(400, gin.H{"error": "请求数据格式错误: " + err.Error()})
		return
	}

	// 更新基本信息
	camera.Type = updateData.Type
	camera.Name = updateData.Name
	camera.Group = updateData.Group
	camera.GatewayId = &updateData.GatewayId
	camera.IntervalSec = updateData.IntervalSec
	camera.LLMModelType = updateData.LLMModelType
	camera.CustomPrompt = updateData.CustomPrompt
	camera.Enabled = updateData.Enabled
	camera.ManualReview = updateData.ManualReview

	// 处理序列号变更
	if updateData.SerialNo != "" && updateData.SerialNo != camera.SerialNo {
		camera.SerialNo = updateData.SerialNo
		if camera.Type == "yingshi" {
			camera.DeviceID = camera.SerialNo
			camera.DeviceName = camera.Name
			camera.ChannelNo = "1"
			camera.Model = "萤石摄像头"
		} else if camera.Type == "hikisc" {
			// 获取海康摄像头详细信息
			if info, err := GetHikCamInfo(camera.SerialNo); err == nil {
				camera.DeviceID = info.DeviceID
				camera.DeviceName = info.Name
				camera.ChannelNo = info.ChannelNo
				camera.Model = info.Model
			}
		} else if camera.Type == VideoPlatformSXGB {
			camera.DeviceID = camera.SerialNo // SIP编码
			camera.DeviceName = camera.Name
			camera.ChannelNo = "1"
			camera.Model = "山西省平台国标流"
		}
	}

	if err := db.GormDb.Save(&camera).Error; err != nil {
		c.JSON(500, gin.H{"error": "更新配置失败: " + err.Error()})
		return
	}

	// 更新定时任务
	taskChanged := oldEnabled != camera.Enabled || oldInterval != camera.IntervalSec
	if taskChanged {
		if err := GTaskManager.UpdateTask(&camera); err != nil {
			log.Warn("更新定时任务失败: %v", err)
		}
	}

	c.JSON(200, gin.H{
		"success": true,
		"data":    camera,
	})
}

func deleteCameraLLMHandler(c *gin.Context) {
	id := c.Param("id")
	// 解析ID
	cameraID, err := strconv.ParseUint(id, 10, 32)
	if err != nil {
		c.JSON(400, gin.H{"error": "无效的ID"})
		return
	}
	GTaskManager.RemoveTask(uint(cameraID))

	if err := db.GormDb.Delete(&db.CameraLLM{}, id).Error; err != nil {
		c.JSON(500, gin.H{"error": "删除失败"})
		return
	}
	c.JSON(200, gin.H{"success": true})
}

func getHikCamInfoHandler(c *gin.Context) {
	serialNo := c.Query("serial_no")
	if serialNo == "" {
		c.JSON(400, gin.H{"error": "序列号不能为空"})
		return
	}

	info, err := GetHikCamInfo(serialNo)
	if err != nil {
		c.JSON(404, gin.H{"error": err.Error()})
		return
	}

	c.JSON(200, gin.H{
		"success": true,
		"data":    info,
	})
}

func getCameraLLMHandler(c *gin.Context) {
	id := c.Param("id")
	var camera db.CameraLLM

	if err := db.GormDb.First(&camera, id).Error; err != nil {
		c.JSON(404, gin.H{"error": "配置未找到"})
		return
	}

	c.JSON(200, gin.H{
		"success": true,
		"data":    camera,
	})
}

func getCameraLLMRecordsHandler(c *gin.Context) {
	id := c.Param("id")
	loc := time.Now().Location()
	// 分页参数
	onlyWarning := c.Query("warning") == "true"
	page, _ := strconv.Atoi(c.DefaultQuery("page", "1"))
	pageSize, _ := strconv.Atoi(c.DefaultQuery("page_size", "10"))
	startStr := c.DefaultQuery("start", time.Now().Add(-24*time.Hour).Format("2006-01-02 15:04:05"))
	endStr := c.DefaultQuery("end", time.Now().Format("2006-01-02 15:04:05"))
	start, _ := time.ParseInLocation("2006-01-02T15:04", startStr, loc)
	end, _ := time.ParseInLocation("2006-01-02T15:04", endStr, loc)
	offset := (page - 1) * pageSize

	var records []db.Recognition
	var total int64

	query := db.GormDb.Model(&db.Recognition{}).
		Where("cmid = ? AND add_time BETWEEN ? AND ?", id, start, end)
	if onlyWarning {
		query = query.Where("warning = ?", true)
	}
	query.
		Count(&total).
		Limit(pageSize).
		Offset(offset).
		Order("add_time desc").
		Find(&records)

	resp := map[string]interface{}{
		"total":   total,
		"records": records,
		"page":    page,
	}
	c.JSON(http.StatusOK, resp)
}

func getCameraLLMListHandler(c *gin.Context) {
	var cameras []db.CameraLLM

	// 获取过滤参数
	cameraType := c.Query("type")
	enabledStr := c.Query("enabled")
	llmModelType := c.Query("llm_model_type")
	cameraIDStr := c.Query("camera_id")
	group := c.Query("group")
	export := c.Query("export")

	// 构建查询条件
	query := db.GormDb.Model(&db.CameraLLM{})

	if cameraType != "" {
		query = query.Where("type = ?", cameraType)
	}

	if enabledStr != "" {
		enabled, err := strconv.ParseBool(enabledStr)
		if err == nil {
			query = query.Where("enabled = ?", enabled)
		}
	}

	if llmModelType != "" {
		query = query.Where("llm_model_type = ?", llmModelType)
	}

	if cameraIDStr != "" {
		cameraID, err := strconv.ParseUint(cameraIDStr, 10, 32)
		if err == nil {
			query = query.Where("id = ?", cameraID)
		}
	}

	if group != "" {
		query = query.Where("\"group\" = ?", group)
	}

	// 执行查询
	if err := query.Find(&cameras).Error; err != nil {
		c.JSON(500, gin.H{"success": false, "error": "查询失败: " + err.Error()})
		return
	}

	// 如果是导出请求，则生成Excel文件
	if export == "excel" {
		exportCameraConfigToExcel(c, cameras)
		return
	}

	c.JSON(200, gin.H{
		"success": true,
		"data":    cameras,
	})
}

// exportCameraConfigToExcel 导出摄像头配置到Excel
func exportCameraConfigToExcel(c *gin.Context, cameras []db.CameraLLM) {
	// 设置响应头，指示浏览器下载文件
	c.Header("Content-Type", "text/csv; charset=utf-8")
	c.Header("Content-Disposition", "attachment; filename=camera_config.csv")

	// 写入CSV头部
	c.String(200, "ID,设备类型,设备名称,序列号,网关编号,点位名称,周期,LLM模型名称,状态,创建时间,报警次数,项目分组\n")

	// 获取每个摄像头的报警次数
	alertCountMap := make(map[uint]int64)
	for _, camera := range cameras {
		var count int64
		db.GormDb.Model(&db.Recognition{}).Where("add_time>? AND cmid = ? AND warning = ?", recognizeBeginTime, camera.ID, true).Count(&count)
		alertCountMap[camera.ID] = count
	}

	// 写入数据行
	for _, camera := range cameras {
		status := "禁用"
		if camera.Enabled {
			status = "启用"
		}

		group := ""
		if camera.Group != "" {
			group = camera.Group
		}

		gatewayId := ""
		if camera.GatewayId != nil {
			gatewayId = *camera.GatewayId
		}

		alertCount := alertCountMap[camera.ID]

		c.String(200, "%d,%s,%s,%s,%s,%s,%d,%s,%s,%s,%d,%s\n",
			camera.ID,
			camera.Type,
			camera.Name,
			camera.SerialNo,
			gatewayId,
			camera.DeviceName,
			camera.IntervalSec,
			camera.LLMModelType,
			status,
			camera.CreatedAt.Format("2006-01-02 15:04:05"),
			alertCount,
			group)
	}
}

func historyHandler(c *gin.Context) {
	id := c.Param("id")
	tmpl, err := template.ParseFiles(htmlHistoryTemplate)
	if err != nil {
		c.JSON(500, gin.H{"error": "模板解析错误"})
		return
	}
	data := struct {
		CameraID string
	}{
		CameraID: id,
	}
	c.Header("Content-Type", "text/html; charset=utf-8")
	_ = tmpl.Execute(c.Writer, data)
}

// RecognitionRecord 用于前端显示的识别记录结构
type RecognitionRecord struct {
	db.Recognition
	CameraName       string
	DescribeDisplay  string
	WebWarning       bool
	WebAudited       bool
	WebAuditeResult  bool
	WebAuditeDisplay string
}

func recognitionsHandler(c *gin.Context) {
	// 获取所有摄像头设备用于筛选
	var cameras []db.CameraLLM
	db.GormDb.Order("id").Find(&cameras)

	// 获取所有不同的分组名称
	var groups []string
	db.GormDb.Model(&db.CameraLLM{}).
		Distinct("group").
		Where("\"group\" != ?", "").
		Order("\"group\"").
		Pluck("group", &groups)

	// 获取第一页数据
	records, currentPage, totalPages := getRecognitionRecords(c, 1)

	// 构造用于前端显示的记录
	displayRecords := buildDisplayRecords(records)

	tmpl, err := template.ParseFiles(htmlRecognitionsTemplate)
	if err != nil {
		c.JSON(500, gin.H{"error": "模板解析错误"})
		return
	}

	data := struct {
		Records     []RecognitionRecord
		Cameras     []db.CameraLLM
		Groups      []string
		CurrentPage int
		TotalPages  int
	}{
		Records:     displayRecords,
		Cameras:     cameras,
		Groups:      groups,
		CurrentPage: currentPage,
		TotalPages:  totalPages,
	}

	c.Header("Content-Type", "text/html; charset=utf-8")
	_ = tmpl.Execute(c.Writer, data)
}

func getRecognitionRecords(c *gin.Context, page int) ([]db.Recognition, int, int) {
	pageSize := 10
	offset := (page - 1) * pageSize

	// 获取查询参数
	startTimeStr := c.Query("start_time")
	endTimeStr := c.Query("end_time")
	warningStr := c.Query("warning")
	cameraIdStr := c.Query("camera_id")
	group := c.Query("group")

	// 构建查询
	query := db.GormDb.Model(&db.Recognition{})

	// 时间筛选
	if startTimeStr != "" {
		startTime, err := time.ParseInLocation("2006-01-02T15:04", startTimeStr, time.Local)
		if err == nil {
			query = query.Where("add_time >= ?", startTime)
		}
	} else {
		query = query.Where("add_time >= ?", time.Now().Add(-24*time.Hour))
	}

	if endTimeStr != "" {
		endTime, err := time.ParseInLocation("2006-01-02T15:04", endTimeStr, time.Local)
		if err == nil {
			query = query.Where("add_time <= ?", endTime)
		}
	} else {
		query = query.Where("add_time <= ?", time.Now())
	}

	// 告警状态筛选
	if warningStr != "" {
		warning, err := strconv.ParseBool(warningStr)
		if err == nil {
			query = query.Where("warning = ?", warning)
		}
	}

	// 设备筛选
	if cameraIdStr != "" {
		cameraId, err := strconv.ParseUint(cameraIdStr, 10, 32)
		if err == nil {
			query = query.Where("cmid = ?", cameraId)
		}
	}

	// 分组筛选
	if group != "" {
		// 先找出属于该分组的所有摄像头ID
		var cameraIds []uint
		db.GormDb.Model(&db.CameraLLM{}).Where("\"group\" = ?", group).Pluck("id", &cameraIds)

		if len(cameraIds) > 0 {
			query = query.Where("cmid IN ?", cameraIds)
		} else {
			// 如果没有找到对应的摄像头，返回空结果
			query = query.Where("cmid IN (?)", []uint{0})
		}
	}

	// 获取总数
	var total int64
	query.Count(&total)

	// 获取分页数据
	var records []db.Recognition
	query.Order("add_time desc").
		Limit(pageSize).
		Offset(offset).
		Find(&records)

	totalPages := int((total + int64(pageSize) - 1) / int64(pageSize))
	if totalPages == 0 {
		totalPages = 1
	}

	return records, page, totalPages
}

func buildDisplayRecords(records []db.Recognition) []RecognitionRecord {
	var displayRecords []RecognitionRecord

	// 获取所有相关摄像头信息
	cameraIds := make([]uint, 0, len(records))
	for _, record := range records {
		cameraIds = append(cameraIds, record.Cmid)
	}

	var cameras []db.CameraLLM
	cameraMap := make(map[uint]db.CameraLLM)
	if len(cameraIds) > 0 {
		db.GormDb.Where("id IN ?", cameraIds).Find(&cameras)
		for _, camera := range cameras {
			cameraMap[camera.ID] = camera
		}
	}

	for _, record := range records {
		displayRecord := RecognitionRecord{
			Recognition:     record,
			CameraName:      "",
			DescribeDisplay: "",
		}

		// 设置摄像头名称
		if camera, exists := cameraMap[record.Cmid]; exists {
			displayRecord.CameraName = camera.Name
		}

		// 设置描述显示
		if record.Describe != nil {
			displayRecord.DescribeDisplay = *record.Describe
		}
		if record.Audited != nil {
			displayRecord.WebAudited = true
			displayRecord.WebAuditeResult = *record.Audited
			if *record.Audited {
				displayRecord.WebAuditeDisplay = "审核通过"
			} else {
				displayRecord.WebAuditeDisplay = "审核不通过"
			}
		} else {
			displayRecord.WebAuditeDisplay = "未审核"
		}

		displayRecords = append(displayRecords, displayRecord)
	}

	return displayRecords
}

func getRecognitionRecordsHandler(c *gin.Context) {
	page, _ := strconv.Atoi(c.DefaultQuery("page", "1"))
	if page < 1 {
		page = 1
	}

	export := c.Query("export")

	// 如果是导出请求，获取所有记录而不是分页记录
	if export == "excel" {
		// 获取所有匹配的记录（不分页）
		var allRecords []db.Recognition

		// 获取查询参数
		startTimeStr := c.Query("start_time")
		endTimeStr := c.Query("end_time")
		cameraIdStr := c.Query("camera_id")
		group := c.Query("group")

		// 构建查询
		query := db.GormDb.Model(&db.Recognition{})

		// 时间筛选
		if startTimeStr != "" {
			startTime, err := time.ParseInLocation("2006-01-02T15:04", startTimeStr, time.Local)
			if err == nil {
				query = query.Where("add_time >= ?", startTime)
			}
		}

		if endTimeStr != "" {
			endTime, err := time.ParseInLocation("2006-01-02T15:04", endTimeStr, time.Local)
			if err == nil {
				query = query.Where("add_time <= ?", endTime)
			}
		}

		// 设备筛选
		if cameraIdStr != "" {
			cameraId, err := strconv.ParseUint(cameraIdStr, 10, 32)
			if err == nil {
				query = query.Where("cmid = ?", cameraId)
			}
		}

		// 分组筛选
		if group != "" {
			// 先找出属于该分组的所有摄像头ID
			var cameraIds []uint
			db.GormDb.Model(&db.CameraLLM{}).Where("\"group\" = ?", group).Pluck("id", &cameraIds)

			if len(cameraIds) > 0 {
				query = query.Where("cmid IN ?", cameraIds)
			} else {
				// 如果没有找到对应的摄像头，返回空结果
				query = query.Where("cmid IN (?)", []uint{0})
			}
		}

		// 获取所有记录
		query.Order("add_time desc").Find(&allRecords)

		// 导出到Excel
		exportRecognitionRecordsToExcel(c, allRecords)
		return
	}

	records, currentPage, totalPages := getRecognitionRecords(c, page)
	displayRecords := buildDisplayRecords(records)

	c.JSON(http.StatusOK, gin.H{
		"success": true,
		"data": map[string]interface{}{
			"records":      displayRecords,
			"current_page": currentPage,
			"total_pages":  totalPages,
		},
	})
}

// 识别记录详情页面处理函数
func recognitionDetailHandler(c *gin.Context) {
	id, _ := strconv.Atoi(c.Param("id"))
	var record = db.Recognition{Id: uint(id)}

	if err := db.GormDb.First(&record).Error; err != nil {
		c.JSON(404, gin.H{"error": "记录未找到"})
		return
	}

	// 获取相关摄像头信息
	var camera db.CameraLLM
	if err := db.GormDb.First(&camera, record.Cmid).Error; err != nil {
		c.JSON(500, gin.H{"error": "获取摄像头信息失败"})
		return
	}

	tmpl, err := template.ParseFiles("recognition_detail.html")
	if err != nil {
		c.JSON(500, gin.H{"error": "模板解析错误"})
		return
	}

	recordView := RecognitionRecord{Recognition: record}
	if record.Audited != nil {
		recordView.WebAudited = true
		recordView.WebAuditeResult = *record.Audited
	}
	if record.Warning != nil {
		recordView.WebWarning = *record.Warning
	}
	data := struct {
		Record RecognitionRecord
		Camera db.CameraLLM
	}{
		Record: recordView,
		Camera: camera,
	}

	c.Header("Content-Type", "text/html; charset=utf-8")
	_ = tmpl.Execute(c.Writer, data)
}

// recognitionImageHandler 以原始字节返回识别图片，并支持浏览器直接下载。
// 使用记录 ID 生成同源地址，避免外部对象存储的跨域下载限制。
func recognitionImageHandler(c *gin.Context) {
	id := c.Param("id")
	var record db.Recognition
	if err := db.GormDb.First(&record, id).Error; err != nil {
		c.JSON(http.StatusNotFound, gin.H{"error": "记录未找到"})
		return
	}
	if record.ImgPath == "" {
		c.JSON(http.StatusNotFound, gin.H{"error": "该记录没有识别图片"})
		return
	}

	imageBytes, err := DownloadImage(record.ImgPath)
	if err != nil {
		log.Warn("读取识别原图失败: %s %s", record.ImgPath, err.Error())
		c.JSON(http.StatusBadGateway, gin.H{"error": "原图读取失败"})
		return
	}

	contentType := http.DetectContentType(imageBytes)
	ext := ""
	switch contentType {
	case "image/jpeg":
		ext = ".jpg"
	case "image/png":
		ext = ".png"
	case "image/gif":
		ext = ".gif"
	case "image/webp":
		ext = ".webp"
	}
	fileName := fmt.Sprintf("recognition_%d%s", record.Id, ext)
	disposition := "inline"
	if c.Query("download") == "1" {
		disposition = "attachment"
	}
	c.Header("Content-Disposition", fmt.Sprintf(`%s; filename="%s"`, disposition, fileName))
	c.Header("X-Content-Type-Options", "nosniff")
	c.Data(http.StatusOK, contentType, imageBytes)
}

func imageDetectionPageHandler(c *gin.Context) {
	tmpl, err := template.ParseFiles(htmlImageDetectionTemplate)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "模板解析错误"})
		return
	}
	c.Header("Content-Type", "text/html; charset=utf-8")
	_ = tmpl.Execute(c.Writer, nil)
}

type imageDetectionRequest struct {
	CameraCode string `json:"camera_code"`
	EventType  string `json:"event_type"`
	PicBase64  string `json:"pic_base64"`
}

// proxyImageDetectionHandler 接收浏览器上传的图片，并转换为检测服务要求的 Base64 JSON。
func proxyImageDetectionHandler(c *gin.Context) {
	c.Request.Body = http.MaxBytesReader(c.Writer, c.Request.Body, maxDetectionImageSize+(1<<20))

	cameraCode := strings.TrimSpace(c.PostForm("camera_code"))
	if cameraCode == "" {
		c.JSON(http.StatusBadRequest, gin.H{"error": "摄像头编码不能为空"})
		return
	}

	eventType := strings.TrimSpace(c.PostForm("event_type"))
	if eventType == "" {
		eventType = "landslide"
	}
	if eventType != "landslide" && eventType != "collapse" {
		c.JSON(http.StatusBadRequest, gin.H{"error": "事件类型仅支持 landslide 或 collapse"})
		return
	}

	fileHeader, err := c.FormFile("image")
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "请选择需要检测的图片"})
		return
	}
	if fileHeader.Size <= 0 || fileHeader.Size > maxDetectionImageSize {
		c.JSON(http.StatusBadRequest, gin.H{"error": "图片大小必须在 20MB 以内"})
		return
	}

	file, err := fileHeader.Open()
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "图片读取失败"})
		return
	}
	defer file.Close()

	imageBytes, err := io.ReadAll(io.LimitReader(file, maxDetectionImageSize+1))
	if err != nil || len(imageBytes) == 0 || len(imageBytes) > maxDetectionImageSize {
		c.JSON(http.StatusBadRequest, gin.H{"error": "图片读取失败或超过 20MB 限制"})
		return
	}
	contentType := http.DetectContentType(imageBytes)
	if !strings.HasPrefix(contentType, "image/") {
		c.JSON(http.StatusBadRequest, gin.H{"error": "上传文件不是有效图片"})
		return
	}

	payload, err := json.Marshal(imageDetectionRequest{
		CameraCode: cameraCode,
		EventType:  eventType,
		PicBase64:  base64.StdEncoding.EncodeToString(imageBytes),
	})
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "检测请求生成失败"})
		return
	}

	request, err := http.NewRequestWithContext(c.Request.Context(), http.MethodPost, imageDetectionURL, bytes.NewReader(payload))
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "检测服务地址配置错误"})
		return
	}
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set("Accept", "application/json")

	client := &http.Client{Timeout: 2 * time.Minute}
	response, err := client.Do(request)
	if err != nil {
		log.Warn("图片检测服务调用失败: %s %v", imageDetectionURL, err)
		c.JSON(http.StatusBadGateway, gin.H{"error": "检测服务调用失败: " + err.Error()})
		return
	}
	defer response.Body.Close()

	responseBody, err := io.ReadAll(io.LimitReader(response.Body, 32<<20))
	if err != nil {
		c.JSON(http.StatusBadGateway, gin.H{"error": "检测结果读取失败"})
		return
	}
	responseContentType := response.Header.Get("Content-Type")
	if responseContentType == "" {
		responseContentType = "application/json; charset=utf-8"
	}
	c.Data(response.StatusCode, responseContentType, responseBody)
}

// 审核识别记录
func auditRecognitionHandler(c *gin.Context) {
	id := c.Param("id")
	var record db.Recognition

	if err := db.GormDb.First(&record, id).Error; err != nil {
		c.JSON(404, gin.H{"error": "记录未找到"})
		return
	}

	if record.Audited != nil {
		c.JSON(400, gin.H{"error": "记录已审核!"})
		return
	}

	// 获取审核参数
	auditedStr := c.PostForm("audited")
	auditUser := c.PostForm("audit_user")

	if auditedStr == "" {
		c.JSON(400, gin.H{"error": "审核状态不能为空"})
		return
	}

	audited, err := strconv.ParseBool(auditedStr)
	if err != nil {
		c.JSON(400, gin.H{"error": "审核状态格式错误"})
		return
	}

	// 更新审核信息
	now := time.Now()
	record.Audited = &audited
	record.AuditTime = &now
	if auditUser != "" {
		record.AuditUser = &auditUser
	}

	if err := db.GormDb.Save(&record).Error; err != nil {
		c.JSON(500, gin.H{"error": "审核失败: " + err.Error()})
		return
	}

	// 调用审核后的处理方法
	if audited {
		log.Info("审核通过，发送告警 %s", record.CameraId)
		go SendAlarm(&record)
		go SendEvent(&record)
	}

	c.JSON(http.StatusOK, gin.H{
		"success": true,
		"message": "审核成功",
		"data":    record,
	})
}

func triggerAlarmApi(c *gin.Context) {
	var data LocalAlarmMsg
	if err := c.ShouldBindJSON(&data); err != nil || data.CameraId == "" {
		c.JSON(400, gin.H{"error": "请求数据格式错误: " + err.Error()})
		return
	}
	log.Info("处理请求：%s", data.String())
	if err := GTaskManager.TriggerLocal(data); err != nil {
		c.JSON(400, gin.H{"error": "识别报错: " + err.Error()})
		return
	}
	c.JSON(200, gin.H{
		"success": true,
	})
}

// exportRecognitionRecordsToExcel 导出识别记录到Excel
func exportRecognitionRecordsToExcel(c *gin.Context, records []db.Recognition) {
	// 设置响应头，指示浏览器下载文件
	c.Header("Content-Type", "text/csv; charset=utf-8")
	c.Header("Content-Disposition", "attachment; filename=recognition_records.csv")

	// 写入CSV头部
	c.String(200, "ID,设备名称,序列号,识别时间,LLM模型名称,大模型复核,复核时间,审核状态,大模型诊断结果,诊断图片\n")

	// 获取所有相关摄像头信息
	cameraIds := make([]uint, 0, len(records))
	for _, record := range records {
		cameraIds = append(cameraIds, record.Cmid)
	}

	var cameras []db.CameraLLM
	cameraMap := make(map[uint]db.CameraLLM)
	if len(cameraIds) > 0 {
		db.GormDb.Where("id IN ?", cameraIds).Find(&cameras)
		for _, camera := range cameras {
			cameraMap[camera.ID] = camera
		}
	}

	// 写入数据行
	for _, record := range records {
		// 获取摄像头名称
		cameraName := ""
		llmMode := ""
		if camera, exists := cameraMap[record.Cmid]; exists {
			cameraName = camera.Name
			llmMode = camera.Model
		} else {
			continue
		}

		// 获取审核状态
		auditStatus := "未审核"
		if record.Audited != nil {
			if *record.Audited {
				auditStatus = "审核通过"
			} else {
				auditStatus = "审核不通过"
			}
		}

		// 获取大模型复核结果
		reviewResult := "正常"
		if record.Warning != nil && *record.Warning {
			reviewResult = "告警"
		}

		// 获取复核时间
		reviewTime := ""
		if record.RecognitionTime != nil {
			reviewTime = record.RecognitionTime.Format("2006-01-02 15:04:05")
		}

		// 获取诊断结果
		describe := ""
		if record.Describe != nil {
			describe = *record.Describe
		}

		// 获取图片路径
		imgPath := record.ImgPath

		// 如果有上传图片路径，优先使用
		if record.UploadImgPath != "" {
			imgPath = record.UploadImgPath
		} else if record.QiniuImgPath != "" {
			imgPath = record.QiniuImgPath
		}

		c.String(200, "%d,%s,%s,%s,%s,%s,%s,%s,%s,%s\n",
			record.Id,
			cameraName,
			record.CameraId,
			record.AddTime.Format("2006-01-02 15:04:05"),
			llmMode,
			reviewResult,
			reviewTime,
			auditStatus,
			describe,
			imgPath)
	}
}
func LogRequestBody() gin.HandlerFunc {
	return func(c *gin.Context) {
		// 读取 body
		body, err := io.ReadAll(c.Request.Body)
		if err != nil {
			log.Warn("读取 body 失败: %v", err)
			c.Next()
			return
		}

		// 打印完整 body
		//log.Info("Request Body: %s", string(body))

		// 重新设置 body，以便后续处理可以继续读取
		c.Request.Body = io.NopCloser(bytes.NewBuffer(body))

		c.Next()
	}
}
func StartWeb() {
	// 创建Gin路由
	r := gin.Default()

	r.Static("/images", LocalBaseDirectory)
	// 路由配置
	r.GET("/", indexHandler)
	r.GET("/recognitions", recognitionsHandler)
	r.GET("/recognitions/list", getRecognitionRecordsHandler)
	r.GET("/image-detection", imageDetectionPageHandler)
	r.POST("/api/image-detection", proxyImageDetectionHandler)
	r.GET("/recognition/:id/image", recognitionImageHandler)
	r.GET("/recognition/:id", recognitionDetailHandler)
	r.POST("/recognition/:id/audit", auditRecognitionHandler)
	r.POST("/camera-llm", createCameraLLMHandler)
	r.GET("/camera-llm/history/:id", historyHandler)
	r.GET("/camera-llm/:id/records", getCameraLLMRecordsHandler)
	r.GET("/camera-llm/:id", getCameraLLMHandler)
	r.PUT("/camera-llm/:id", updateCameraLLMHandler)
	r.DELETE("/camera-llm/:id", deleteCameraLLMHandler)
	r.GET("/camera-llm/list", getCameraLLMListHandler)
	r.GET("/api/hik-cam-info", getHikCamInfoHandler)
	r.POST("/api/alarm", LogRequestBody(), triggerAlarmApi)

	fmt.Println("服务器启动在 http://0.0.0.0:8080")
	go r.Run(":8080")
}
