package db

import (
	"base/log"
	"base/utils"
	"gorm.io/driver/postgres"
	"gorm.io/gorm"
	"time"
)

var GormDb *gorm.DB

func init() {
	checkErr(InitDb())
}

const (
	keepAliveInterval = 1 * time.Second
	connMaxLifetime   = 10 * time.Minute
	connMaxIdleTime   = 5 * time.Minute
	maxIdleConns      = 2
	maxOpenConns      = 10
)

func InitDb() error {
	var err error
	dsn := utils.GIniParser.GetString("db", "dsn")
	if dsn != "" {
		GormDb, err = gorm.Open(postgres.Open(dsn), &gorm.Config{})
		if err != nil {
			log.Error("connect db err: %s", err.Error())
			return err
		}
		sqlDB, sqlErr := GormDb.DB()
		if sqlErr != nil {
			err = sqlErr
			log.Warn("[DB] 获取底层连接失败: %v", err)
		} else {
			sqlDB.SetMaxIdleConns(maxIdleConns)
			sqlDB.SetMaxOpenConns(maxOpenConns)
			sqlDB.SetConnMaxLifetime(connMaxLifetime)
			sqlDB.SetConnMaxIdleTime(connMaxIdleTime)
			// 尝试 Ping 验证连接
			pingErr := sqlDB.Ping()
			if pingErr == nil {
				log.Info("[DB] 数据库连接成功")
			} else {
				log.Warn("[DB] Ping 失败: %v", err)
				time.Sleep(10 * time.Second)
				return pingErr
			}
		}
		KeepAlive(GormDb, keepAliveInterval)
	}

	checkErr(GormDb.AutoMigrate(&Recognition{}, &CameraLLM{}, &RecognitionAlert{}, &YingshiSecret{}))
	return nil
}

func KeepAlive(db *gorm.DB, interval time.Duration) {
	go func() {
		ticker := time.NewTicker(interval)
		defer ticker.Stop()

		sqlDB, _ := db.DB()
		for range ticker.C {
			if err := sqlDB.Ping(); err != nil {
				log.Warn("[DB] Ping 失败，可能已断开: %v", err)
			}
		}
	}()
}

func checkErr(err error) {
	if err != nil {
		panic(err)
	}
}

/*
ALTER TABLE recognition
ADD CONSTRAINT fk_cmid
FOREIGN KEY (cmid) REFERENCES camera_llms(id);
*/
// Recognition 识别记录
type Recognition struct {
	Id              uint `gorm:"primaryKey"` // 主键
	Cmid            uint `gorm:"index"`      // 外键：识别任务ID，引用 CameraLLM.ID，并加索引
	ImgPath         string
	CameraId        string    `gorm:"type:varchar(100);index"`
	Warning         *bool     `gorm:"type:bool"`
	Describe        *string   `gorm:"type:text"`
	AddTime         time.Time `gorm:"autoCreateTime;not null;index"` // 创建时间
	Model           *string
	RecognitionTime *time.Time
	PhotoBytes      []byte `gorm:"-"`
	Prompt          string `gorm:"-"`
	UploadImgPath   string // 上报到路安
	QiniuImgPath    string

	// 审核字段
	Audited   *bool      `gorm:"type:bool;default:null"` // 审核状态：nil-未审核，true-审核通过，false-审核不通过
	AuditTime *time.Time // 审核时间
	AuditUser *string    `gorm:"type:varchar(100)"` // 审核人
}

type RecognitionAlert struct {
	Id              uint `gorm:"primaryKey"` // 主键
	Cmid            uint `gorm:"index"`      // 外键：识别任务ID，引用 CameraLLM.ID，并加索引
	ImgPath         string
	CameraId        string    `gorm:"type:varchar(100);index"`
	Describe        *string   `gorm:"type:text"`
	AddTime         time.Time `gorm:"autoCreateTime;not null;index"` // 创建时间
	RecognitionTime *time.Time

	AlarmType     string
	Prompt        string
	UploadImgPath string
	Topic         string
	Content       string
	PubRes        bool
}

// CameraLLM 摄像头LLM任务配置表
type CameraLLM struct {
	ID           uint      `gorm:"primaryKey" json:"id"`
	Type         string    `gorm:"size:16;not null" json:"type"`           // 设备类型：yingshi hikisc
	Name         string    `gorm:"size:128" json:"name"`                   // 名称
	DeviceID     string    `gorm:"size:128" json:"device_id"`              // 设备ID
	DeviceName   string    `gorm:"size:128" json:"device_name"`            // 设备名称
	SerialNo     string    `gorm:"size:128" json:"serial_no"`              // 设备序列号
	GatewayId    *string   `gorm:"size:128" json:"gateway_id"`             // 网关ID
	Model        string    `gorm:"size:128" json:"model"`                  // 型号
	ChannelNo    string    `gorm:"size:128;default:'1'" json:"channel_no"` // 通道号 默认1
	StreamUrl    string    `gorm:"type:text" json:"stream_url"`            // 播放地址（国标流等，由定时任务更新）
	IntervalSec  int       `json:"interval_sec"`                           // 检查周期（秒）
	LLMModelType string    `gorm:"size:64" json:"llm_model_type"`          // LLM模型类型
	CustomPrompt string    `gorm:"type:text" json:"custom_prompt"`         // 自定义提示词
	Enabled      bool      `gorm:"default:true" json:"enabled"`            // 是否启用
	ManualReview bool      `gorm:"default:false" json:"manual_review"`     // 是否需要人工审核
	Group        string    `gorm:"size:128" json:"group"`                  // 项目分组
	CreatedAt    time.Time `json:"created_at"`
	UpdatedAt    time.Time `json:"updated_at"`
	SecretID     *uint     `gorm:"index" json:"secret_id"` // 萤石云密钥ID（外键）

	Recognitions  []Recognition  `gorm:"foreignKey:Cmid;references:ID;constraint:OnUpdate:CASCADE,OnDelete:CASCADE" json:"recognitions"`
	YingshiSecret *YingshiSecret `gorm:"foreignKey:SecretID;references:ID" json:"yingshi_secret,omitempty"`
}

// YingshiSecret 萤石云配置密钥表
type YingshiSecret struct {
	ID        uint      `gorm:"primaryKey" json:"id"`
	Name      string    `gorm:"size:128;not null" json:"name"`       // 配置名称
	AppKey    string    `gorm:"size:256;not null" json:"app_key"`    // 萤石云AppKey
	AppSecret string    `gorm:"size:256;not null" json:"app_secret"` // 萤石云AppSecret
	Remark    string    `gorm:"size:256" json:"remark"`              // 备注
	CreatedAt time.Time `json:"created_at"`
	UpdatedAt time.Time `json:"updated_at"`
}
