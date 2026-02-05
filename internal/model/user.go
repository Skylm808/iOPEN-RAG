package model

import "time"

// User 对应于数据库中的 'users' 表
type User struct {
	ID         uint      `gorm:"primaryKey;autoIncrement" json:"id"`
	Username   string    `gorm:"type:varchar(255);not null;unique" json:"username"`
	Password   string    `gorm:"type:varchar(255);not null" json:"-"` // Hide password in json output
	Role       string    `gorm:"type:enum('USER', 'ADMIN');default:'USER'" json:"role"`
	OrgTags    string    `gorm:"type:varchar(255)" json:"orgTags"`
	PrimaryOrg string    `gorm:"type:varchar(50)" json:"primaryOrg"`
	CreatedAt  time.Time `gorm:"autoCreateTime" json:"createdAt"`
	UpdatedAt  time.Time `gorm:"autoUpdateTime" json:"updatedAt"`
}

// TableName 指定 GORM 使用的表名
func (User) TableName() string {
	return "users"
}
