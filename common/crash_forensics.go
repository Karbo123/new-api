package common

// 进程级崩溃取证：New API 曾出现「用某个模型时整个程序静默退出」的现象，
// 但部署方式是隐藏窗口启动、stderr 无处可去——panic 栈直接丢失，事后无法
// 定位。这里做两件事：
//  1. InstallCrashForensics：把 stderr 重定向到日志目录下的 crash 文件，
//     并注册 panic 兜底（Go 运行时打印 panic 时写的就是 stderr）；
//  2. GoSafe：包装后台 goroutine，把 panic 连同完整栈写进 crash 日志后
//     只让该 goroutine 结束，而不是拖垮整个进程。

import (
	"fmt"
	"os"
	"path/filepath"
	"runtime/debug"
	"time"

	"github.com/gin-gonic/gin"
)

var crashLogPath = ""

// InstallCrashForensics 把 stderr 指向 <logdir>/crash.<date>.log 并记录路径。
// 必须在日志系统初始化之后、启动 goroutine 之前调用。
func InstallCrashForensics(logDir string) {
	if logDir == "" {
		logDir = "logs"
	}
	if err := os.MkdirAll(logDir, 0755); err != nil {
		SysLog("crash forensics: mkdir failed: " + err.Error())
		return
	}
	crashLogPath = filepath.Join(logDir, fmt.Sprintf("crash.%s.log", time.Now().Format("20060102")))
	f, err := os.OpenFile(crashLogPath, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0644)
	if err != nil {
		SysLog("crash forensics: open failed: " + err.Error())
		return
	}
	// os.Stderr 一起改：Go 运行时写 panic 栈用的是真实的 fd 2 还是 os.Stderr，
	// 两者都覆盖才能确保落盘
	os.Stderr = f
	gin.DefaultErrorWriter = f
	SysLog("crash forensics enabled: panic/stderr → " + crashLogPath)
}

// CrashLogPath 返回当前 crash 日志路径（未启用时为空）
func CrashLogPath() string { return crashLogPath }

// ReportPanic 把一次 panic（含栈）写进 crash 日志；供各 goroutine 的
// recover 分支调用。写失败也不抛（绝不能在错误处理路径上再 panic）
func ReportPanic(where string, r any) {
	stack := debug.Stack()
	ts := time.Now().Format("2006/01/02 - 15:04:05")
	msg := fmt.Sprintf("\n[PANIC] %s | where=%s | value=%v\n%s\n", ts, where, r, stack)
	LogWriterMu.RLock()
	defer LogWriterMu.RUnlock()
	if _, err := fmt.Fprint(gin.DefaultErrorWriter, msg); err != nil {
		// 兜底：直接写 crash 文件（若已配置）
		if crashLogPath != "" {
			if f, e := os.OpenFile(crashLogPath, os.O_WRONLY|os.O_APPEND, 0644); e == nil {
				_, _ = f.WriteString(msg)
				_ = f.Close()
			}
		}
	}
}

// GoSafe 启动一个带 panic 兜底的后台 goroutine：panic 会写进 crash 日志，
// 该 goroutine 结束但进程存活。用于任何「不能让单次故障拖垮网关」的后台任务
func GoSafe(where string, fn func()) {
	go func() {
		defer func() {
			if r := recover(); r != nil {
				ReportPanic(where, r)
			}
		}()
		fn()
	}()
}

// RecoverTo 在 defer 中使用：recover 到 panic 后写入 crash 日志。
// 用法：defer common.RecoverTo("relay/xxx")
func RecoverTo(where string) {
	if r := recover(); r != nil {
		ReportPanic(where, r)
	}
}
