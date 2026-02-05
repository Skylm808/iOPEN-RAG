// Package repository 定义了与数据库进行数据交换的接口和实现。
package repository

import (
	"gorm.io/gorm"
	"pai-smart-go/internal/model"
)

// UserRepository 接口定义了用户数据的持久化操作。
type UserRepository interface {
	Create(user *model.User) error
	FindByUsername(username string) (*model.User, error)
	Update(user *model.User) error
	FindAll() ([]model.User, error)
	FindWithPagination(offset, limit int) ([]model.User, int64, error)
	FindByID(userID uint) (*model.User, error)
}

// userRepository 是 UserRepository 接口的 GORM 实现。
type userRepository struct {
	db *gorm.DB
}

// NewUserRepository 创建一个新的 UserRepository 实例。
func NewUserRepository(db *gorm.DB) UserRepository {
	return &userRepository{db: db}
}

// Create 在数据库中创建一个新的用户记录。
func (r *userRepository) Create(user *model.User) error {
	return r.db.Create(user).Error
}

// FindByUsername 根据用户名从数据库中查找一个用户。
func (r *userRepository) FindByUsername(username string) (*model.User, error) {
	var user model.User
	err := r.db.Where("username = ?", username).First(&user).Error
	if err != nil {
		return nil, err
	}
	return &user, nil
}

// Update 更新数据库中一个已存在的用户记录。
func (r *userRepository) Update(user *model.User) error {
	return r.db.Save(user).Error
}

// FindAll 从数据库中检索所有用户记录。
func (r *userRepository) FindAll() ([]model.User, error) {
	var users []model.User
	err := r.db.Find(&users).Error
	return users, err
}

// FindWithPagination 从数据库中分页检索用户记录。
// 它返回用户列表、总记录数和可能发生的错误。
func (r *userRepository) FindWithPagination(offset, limit int) ([]model.User, int64, error) {
	var users []model.User
	var total int64

	db := r.db.Model(&model.User{})

	// 首先计算总记录数
	err := db.Count(&total).Error
	if err != nil {
		return nil, 0, err
	}

	// 然后根据偏移量和限制获取当前页的数据
	err = db.Offset(offset).Limit(limit).Find(&users).Error
	if err != nil {
		return nil, 0, err
	}

	return users, total, nil
}

// FindByID 根据用户 ID 从数据库中查找一个用户。
func (r *userRepository) FindByID(userID uint) (*model.User, error) {
	var user model.User
	err := r.db.First(&user, userID).Error
	if err != nil {
		return nil, err
	}
	return &user, nil
}
