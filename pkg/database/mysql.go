package database

import (
	"gorm.io/driver/mysql"
	"gorm.io/gorm"
	"pai-smart-go/pkg/log"
	"time"
)

var DB *gorm.DB

// InitMySQL 初始化 MySQL 数据库连接
func InitMySQL(dsn string) {
	var err error
	DB, err = gorm.Open(mysql.Open(dsn), &gorm.Config{
		// 可以在这里添加 GORM 的配置
	})
	if err != nil {
		log.Fatal("failed to connect database", err)
	}

	// 配置连接池
	sqlDB, err := DB.DB()
	if err != nil {
		log.Fatal("failed to get sql.DB", err)
	}

	sqlDB.SetMaxIdleConns(10)           // 设置空闲连接池中连接的最大数量
	sqlDB.SetMaxOpenConns(100)          // 设置打开数据库连接的最大数量
	sqlDB.SetConnMaxLifetime(time.Hour) // 设置了连接可复用的最大时间

	log.Info("MySQL database connected successfully")
}
