package log

import (
	"os"

	"go.uber.org/zap"
	"go.uber.org/zap/zapcore"
)

var sugar *zap.SugaredLogger

// Init 初始化 zap logger
func Init(level, format, outputPath string) {
	var err error
	var logger *zap.Logger
	var zapConfig zap.Config

	// 根据配置设置日志级别
	logLevel := zap.NewAtomicLevel()
	if err := logLevel.UnmarshalText([]byte(level)); err != nil {
		logLevel.SetLevel(zap.InfoLevel)
	}

	// 根据配置设置编码格式
	encoding := "json"
	if format == "console" {
		encoding = "console"
	}

	// 开发环境配置
	if format == "console" {
		zapConfig = zap.NewDevelopmentConfig()
		zapConfig.EncoderConfig.EncodeLevel = zapcore.CapitalColorLevelEncoder
	} else {
		// 生产环境配置
		zapConfig = zap.NewProductionConfig()
	}

	zapConfig.Level = logLevel
	zapConfig.Encoding = encoding
	zapConfig.OutputPaths = []string{"stdout"}
	if outputPath != "" {
		// 如果指定了文件输出路径，同时输出到文件和 stdout
		// 确保目录存在
		_ = os.MkdirAll(outputPath, os.ModePerm)
		zapConfig.OutputPaths = append(zapConfig.OutputPaths, outputPath+"/app.log")
	}

	// 构建 logger
	logger, err = zapConfig.Build()
	if err != nil {
		panic(err)
	}

	// 将 Logger 转换为 SugaredLogger
	sugar = logger.Sugar()
}

// Info 记录一条 info 级别的日志
func Info(msg string) {
	sugar.Info(msg)
}

// Infof 使用格式化字符串记录一条 info 级别的日志
func Infof(template string, args ...interface{}) {
	sugar.Infof(template, args...)
}

// Infow 使用键值对记录一条 info 级别的结构化日志。
// 这是记录复杂上下文信息的首选方法。
func Infow(msg string, keysAndValues ...interface{}) {
	sugar.Infow(msg, keysAndValues...)
}

// Warnf 使用格式化字符串记录一条 warn 级别的日志
func Warnf(template string, args ...interface{}) {
	sugar.Warnf(template, args...)
}

// Error 记录一条 error 级别的日志，并附带 error 信息
func Error(msg string, err error) {
	sugar.Errorw(msg, "error", err)
}

// Fatal 记录一条 fatal 级别的日志，并附带 error 信息，然后退出程序
func Fatal(msg string, err error) {
	sugar.Fatalw(msg, "error", err)
}

func Fatalf(template string, args ...interface{}) {
	sugar.Fatalf(template, args...)
}

func Errorf(template string, args ...interface{}) {
	sugar.Errorf(template, args...)
}

// Sync 将缓冲区中的任何日志刷新（写入）到底层 Writer。
// 在程序退出前调用它是个好习惯。
func Sync() {
	_ = sugar.Sync()
}
