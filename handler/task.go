package handler

import (
	"base/log"
	"fmt"
	"github.com/robfig/cron/v3"
	"gorm.io/gorm"
	"os"
	"strings"
	"sync"
	"time"
	"video-recognition/db"
)

var GTaskManager *TaskManager

func InitTaskManager() {
	if GTaskManager == nil {
		GTaskManager = newTaskManager(db.GormDb)
		GTaskManager.Start()
	}
}

// TaskManager 任务管理器
type TaskManager struct {
	cron  *cron.Cron
	tasks map[uint]cron.EntryID // camera_llm_id -> cron entry id
	mutex sync.RWMutex
	db    *gorm.DB
}

// NewTaskManager 创建任务管理器
func newTaskManager(database *gorm.DB) *TaskManager {
	c := cron.New(cron.WithSeconds()) // 支持秒级别的cron表达式
	return &TaskManager{
		cron:  c,
		tasks: make(map[uint]cron.EntryID),
		db:    database,
	}
}

// Start 启动任务管理器
func (tm *TaskManager) Start() {
	_, _ = tm.cron.AddFunc("0 0 2 * * *", func() {
		tm.checkDeviceNo()
	})

	// 定时刷新"山西省平台国标流"摄像头的播放地址
	sxgbCronExpr := fmt.Sprintf("0 */%d * * * *", sxgbIntervalMin)
	if _, err := tm.cron.AddFunc(sxgbCronExpr, RefreshSxgbStreamUrls); err != nil {
		log.Warn("添加国标流地址刷新任务失败: %v", err)
	}

	tm.cron.Start()
	go RefreshSxgbStreamUrls() // 启动时立即执行一次，避免等待首个周期
	log.Info("任务管理器已启动")
}

// Stop 停止任务管理器
func (tm *TaskManager) Stop() {
	tm.cron.Stop()
	log.Info("任务管理器已停止")
}

// AddTask 添加任务
func (tm *TaskManager) AddTask(camera *db.CameraLLM) error {
	tm.mutex.Lock()
	defer tm.mutex.Unlock()

	// 国标流类型仅用于展示播放地址（由独立的定时任务刷新），不参与抓拍识别定时任务
	if camera.Type == VideoPlatformSXGB {
		return nil
	}

	if os.Getenv("LLM_MONITOR") != "true" {
		//if !strings.Contains(camera.CustomPrompt, "#test") {
		log.Info("LLM_MONITOR=false SKIP")
		return nil
		//}
	}

	// 如果任务已存在，先移除
	if entryID, exists := tm.tasks[camera.ID]; exists {
		tm.cron.Remove(entryID)
		delete(tm.tasks, camera.ID)
	}

	// 如果未启用，不添加任务
	if !camera.Enabled {
		return nil
	}

	// 生成Cron表达式
	cronExpr := tm.generateCronExpression(camera.IntervalSec)

	// 添加任务
	entryID, err := tm.cron.AddFunc(cronExpr, func() {
		tm.executeTask(camera.ID)
	})

	if err != nil {
		return fmt.Errorf("添加定时任务失败: %v", err)
	}

	// 保存任务ID
	tm.tasks[camera.ID] = entryID

	log.Info("为摄像头 %s (ID: %d) 添加定时任务，周期: %d秒, Cron: %s",
		camera.Name, camera.ID, camera.IntervalSec, cronExpr)

	return nil
}

func (tm *TaskManager) checkDeviceNo() {
	var cameraLLMs []db.CameraLLM
	db.GormDb.Find(&cameraLLMs)
	GetHikCamsV2()
	for _, m := range cameraLLMs {
		info, err := GetHikCamInfo(m.DeviceID)
		if err != nil {
			log.Info("check read: %s", m.DeviceID)
			hi := GetHikCamFromName(m.Name)
			if hi != nil {
				if m.SerialNo != hi.SerialNo || m.DeviceID != hi.DeviceID {
					m.SerialNo = hi.SerialNo
					m.DeviceID = hi.DeviceID
					if err := db.GormDb.Save(&m).Error; err != nil {
						log.Info("check save err: %s %s %s", m.DeviceID, m.SerialNo, err.Error())
						continue
					}
					log.Info("check updated!!! %s", m.SerialNo)
				}
			}
			continue
		}
		if info.SerialNo == m.SerialNo {
			continue
		}
		m.SerialNo = info.SerialNo
		if err := db.GormDb.Save(&m).Error; err != nil {
			log.Info("check save err: %s %s %s", m.DeviceID, m.SerialNo, err.Error())
			continue
		}
		log.Info("check updated!!! %s", m.SerialNo)
	}
}

// RemoveTask 移除任务
func (tm *TaskManager) RemoveTask(cameraID uint) {
	tm.mutex.Lock()
	defer tm.mutex.Unlock()

	if entryID, exists := tm.tasks[cameraID]; exists {
		tm.cron.Remove(entryID)
		delete(tm.tasks, cameraID)
		log.Info("移除摄像头任务 (ID: %d)", cameraID)
	}
}

// UpdateTask 更新任务
func (tm *TaskManager) UpdateTask(camera *db.CameraLLM) error {
	// 先移除旧任务
	tm.RemoveTask(camera.ID)
	// 添加新任务
	return tm.AddTask(camera)
}

// generateCronExpression 根据间隔秒数生成Cron表达式
func (tm *TaskManager) generateCronExpression(intervalSec int) string {
	if intervalSec < 60 {
		// 小于1分钟，使用秒级表达式 (每N秒执行一次)
		return fmt.Sprintf("*/%d * * * * *", intervalSec)
	} else if intervalSec < 3600 {
		// 小于1小时，使用分钟级表达式
		minutes := intervalSec / 60
		return fmt.Sprintf("0 */%d * * * *", minutes)
	} else if intervalSec < 86400 {
		// 小于1天，使用小时级表达式
		hours := intervalSec / 3600
		return fmt.Sprintf("0 0 */%d * * *", hours)
	} else {
		// 大于等于1天，使用天级表达式
		days := intervalSec / 86400
		if days == 1 {
			return "0 0 0 * * *" // 每天执行一次
		} else {
			return fmt.Sprintf("0 0 0 */%d * *", days)
		}
	}
}

// getNextRunTime 获取下次执行时间
func (tm *TaskManager) getNextRunTime(cronExpr string) *time.Time {
	if schedule, err := cron.ParseStandard(cronExpr); err == nil {
		next := schedule.Next(time.Now())
		return &next
	}
	return nil
}

// executeTask 执行任务
func (tm *TaskManager) executeTask(cameraID uint) {
	log.Info("开始执行摄像头任务 (ID: %d)", cameraID)

	// 获取摄像头配置
	var camera db.CameraLLM
	if err := tm.db.Preload("YingshiSecret").First(&camera, cameraID).Error; err != nil {
		log.Info("获取摄像头配置失败 (ID: %d): %v", cameraID, err)
		return
	}

	var cameraNo = camera.SerialNo
	if cameraNo == "" {
		cameraNo = camera.DeviceID
	}
	var cameraName = camera.Name
	// 检查是否仍然启用
	if !camera.Enabled {
		log.Info("摄像头已禁用，跳过任务 (ID: %d) (%s) (%s)", cameraID, cameraName, cameraNo)
		tm.RemoveTask(cameraID)
		return
	}

	// 执行具体的LLM检测任务
	if err := tm.performLLMDetection(&camera, nil); err != nil {
		log.Warn("LLM检测任务执行失败 (ID: %d) (%s) (%s): %v", cameraID, cameraName, cameraNo, err)
	} else {
		log.Info("LLM检测任务执行成功 (ID: %d) (%s) (%s)", cameraID, cameraName, cameraNo)
	}
}

// LocalAlarmMsg 本地模型触发消息
type LocalAlarmMsg struct {
	CameraId        string `json:"cameraId"`        // GA8064073
	AlarmType       string `json:"alarmType"`       // landslide
	RecognitionTime int64  `json:"recognitionTime"` // 识别时间毫秒时间戳
	PhotoBytes      []byte `json:"photoBytes"`      // 图片数据
	Sure            bool   `json:"sure"`            // 确定为异常情况
}

func (m LocalAlarmMsg) String() string {
	return fmt.Sprintf(
		"LocalAlarmMsg{CameraId:%s, AlarmType:%s, RecognitionTime:%d, Sure:%t}",
		m.CameraId,
		m.AlarmType,
		m.RecognitionTime,
		m.Sure,
	)
}

// 本地模型判断告警后调用触发
func (tm *TaskManager) TriggerLocal(msg LocalAlarmMsg) (err error) {
	defer func() {
		if r := recover(); r != nil {
			err = fmt.Errorf("%v", r)
		}
	}()
	var camera db.CameraLLM
	if err = tm.db.Where("serial_no=? and llm_model_type=?", msg.CameraId, msg.AlarmType).First(&camera).Error; err != nil {
		log.Info("获取摄像头配置失败 (serial_no: %s llm_model_type:%s): %v", msg.CameraId, msg.AlarmType, err)
		return
	}
	var cameraNo = camera.SerialNo
	if cameraNo == "" {
		cameraNo = camera.DeviceID
	}
	var cameraName = camera.Name
	if err = tm.performLLMDetection(&camera, &msg); err != nil {
		log.Warn("[外部触发] LLM检测任务执行失败 (ID: %d) (%s) (%s): %v", camera.ID, cameraName, cameraNo, err)
	} else {
		log.Info("[外部触发] LLM检测任务执行成功 (ID: %d) (%s) (%s)", camera.ID, cameraName, cameraNo)
	}
	return
}

// performLLMDetection 执行LLM检测
func (tm *TaskManager) performLLMDetection(camera *db.CameraLLM, msg *LocalAlarmMsg) error {
	// 这里实现具体的LLM检测逻辑
	log.Info("正在对摄像头 %s 执行 %s 类型的LLM检测", camera.Name, camera.LLMModelType)

	prompt := ""
	// 模拟检测过程
	switch camera.LLMModelType {
	case "landslide":
		prompt = `当前图像为山体或道路摄像头监测画面，请判断是否存在滑坡、边坡崩塌或落石灾害。
重点检查滑坡体、新鲜裸露土石、坡体断裂或明显位移、大量岩石泥土堆积、倒伏树木，以及道路被掩埋、阻断或受到明显威胁等证据。
只要灾害主体和上述证据清晰可见，即使远处或局部存在云雾、天空过曝、阴影、雨水或低照度，也必须判定 warning:true，不得仅因局部环境或画质问题将明确灾害判为正常。
只有未发现明确灾害证据时才判定 warning:false。describe 字段只输出一句话结论（不超过30字），例如'图像显示山体发生大规模滑坡，道路被掩埋'，不要输出检查过程或细节罗列。`
	case "collapse":
		prompt = "当前图像内容为摄像头监测画面,判断图像中是否存在坍塌现象。检测桥梁结构整体或局部坍塌事件,包括但不限于桥面断裂、桥墩台沉降、桥体错位、桥板断裂等典型破坏特征。若存在坍塌，describe 字段只输出一句话结论（不超过30字），例如'图像显示一座大型桥梁发生严重坍塌事故'，不要输出细节描述。"
	case "fire":
		prompt = "当前图像内容为摄像头监测画面,判断图像中是否存在火灾现象。若存在火灾，describe 字段只输出一句话结论（不超过30字），例如'图像显示一处建筑发生火灾，浓烟明显'，不要输出细节描述。"
	case "flood":
		prompt = "当前图像内容为摄像头监测画面,判断图像中是否存在洪水现象。若存在洪水，describe 字段只输出一句话结论（不超过30字），例如'图像显示河道洪水泛滥，道路被淹没'，不要输出细节描述。"
	case "custom":
	default:
		return fmt.Errorf("未知的LLM模型类型: %s", camera.LLMModelType)
	}
	if strings.TrimSpace(camera.CustomPrompt) != "" {
		prompt = camera.CustomPrompt
	}
	log.Info("执行 %s 检测 - 设备: %s, 序列号: %s, 提示词: %s", camera.LLMModelType, camera.Name, camera.SerialNo, prompt)

	return Recognition(camera, prompt, msg)
}

// LoadExistingTasks 加载现有任务
func (tm *TaskManager) LoadExistingTasks() error {
	var cameras []db.CameraLLM
	if err := tm.db.Model(&db.CameraLLM{}).Where("enabled = ?", true).Find(&cameras).Error; err != nil {
		return fmt.Errorf("加载现有任务失败: %v", err)
	}

	for _, camera := range cameras {
		if err := tm.AddTask(&camera); err != nil {
			log.Warn("加载任务失败 (ID: %d): %v", camera.ID, err)
		}
	}

	log.Info("已加载 %d 个现有任务", len(cameras))
	return nil
}
