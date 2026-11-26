package main

import (
	"base/log"
	"os"
	"os/signal"
	"syscall"
	"time"
	"video-recognition/handler"
)

func main() {
	ender := make(chan os.Signal, 1)
	signal.Notify(ender, os.Interrupt, os.Kill, syscall.SIGTERM)
	begin := time.Now()
	// 初始化任务管理器
	handler.InitTaskManager()
	// 加载现有任务
	if err := handler.GTaskManager.LoadExistingTasks(); err != nil {
		log.Warn("加载现有任务失败: %v", err)
	}

	go handler.StartWeb()
	log.Info("STARTED. Total Cost %d ms", time.Since(begin).Milliseconds())
	<-ender
}
