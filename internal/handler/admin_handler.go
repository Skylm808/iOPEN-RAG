// Package handler 包含了处理 HTTP 请求的控制器逻辑。
package handler

import (
	"net/http"
	"pai-smart-go/internal/service"
	"pai-smart-go/pkg/log"
	"pai-smart-go/pkg/token"
	"strconv"
	"time"

	"github.com/gin-gonic/gin"
)

// AdminHandler 负责处理所有与管理员相关的 API 请求。
type AdminHandler struct {
	adminService service.AdminService
	userService  service.UserService
}

// NewAdminHandler 创建一个新的 AdminHandler 实例。
func NewAdminHandler(adminService service.AdminService, userService service.UserService) *AdminHandler {
	return &AdminHandler{
		adminService: adminService,
		userService:  userService,
	}
}

// CreateOrgTagRequest 定义了创建组织标签 API 的请求体结构。
type CreateOrgTagRequest struct {
	TagID       string `json:"tagId" binding:"required"`
	Name        string `json:"name" binding:"required"`
	Description string `json:"description"`
	ParentTag   string `json:"parentTag"`
}

// CreateOrganizationTag 处理创建新组织标签的请求。
func (h *AdminHandler) CreateOrganizationTag(c *gin.Context) {
	var req CreateOrgTagRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		log.Warnf("CreateOrganizationTag: Invalid request payload, error: %v", err)
		c.JSON(http.StatusBadRequest, gin.H{"code": http.StatusBadRequest, "message": "无效的请求负载", "data": nil})
		return
	}

	// 从上下文中获取管理员用户信息
	claimsValue, _ := c.Get("claims")
	claims := claimsValue.(*token.CustomClaims)

	// 获取完整的创建者用户信息
	creator, err := h.userService.GetProfile(claims.Username)
	if err != nil {
		log.Error("CreateOrganizationTag: Creator user not found", err)
		c.JSON(http.StatusNotFound, gin.H{"code": http.StatusNotFound, "message": "创建者用户未找到", "data": nil})
		return
	}

	// 调用 service 层执行创建逻辑
	tag, err := h.adminService.CreateOrganizationTag(req.TagID, req.Name, req.Description, req.ParentTag, creator)
	if err != nil {
		log.Error("CreateOrganizationTag: Failed to create organization tag", err)
		if err.Error() == "tagID 已存在" {
			c.JSON(http.StatusInternalServerError, gin.H{"code": http.StatusInternalServerError, "message": "tagID 已存在", "data": nil})
			return
		}
		c.JSON(http.StatusInternalServerError, gin.H{"code": http.StatusInternalServerError, "message": "创建组织标签失败", "data": nil})
		return
	}

	log.Infof("Admin user '%s' created organization tag '%s'", claims.Username, req.TagID)
	c.JSON(http.StatusOK, gin.H{"code": http.StatusOK, "message": "success", "data": tag})
}

// ListOrganizationTags 处理获取所有组织标签列表的请求。
func (h *AdminHandler) ListOrganizationTags(c *gin.Context) {
	tags, err := h.adminService.ListOrganizationTags()
	if err != nil {
		log.Error("ListOrganizationTags: Failed to list organization tags", err)
		c.JSON(http.StatusInternalServerError, gin.H{"code": http.StatusInternalServerError, "message": "获取组织标签列表失败", "data": nil})
		return
	}
	c.JSON(http.StatusOK, gin.H{"code": http.StatusOK, "message": "success", "data": tags})
}

// GetOrganizationTagTree handles the request to get the organization tag tree.
func (h *AdminHandler) GetOrganizationTagTree(c *gin.Context) {
	tree, err := h.adminService.GetOrganizationTagTree()
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"code": http.StatusInternalServerError, "message": "Failed to get organization tag tree", "data": nil})
		return
	}
	c.JSON(http.StatusOK, gin.H{"code": http.StatusOK, "message": "success", "data": tree})
}

// GetAllConversations handles the request to get all conversation histories.
func (h *AdminHandler) GetAllConversations(c *gin.Context) {
	// Parse optional userid
	var userID *uint
	if userIDStr := c.Query("userid"); userIDStr != "" {
		id, err := strconv.ParseUint(userIDStr, 10, 32)
		if err != nil {
			c.JSON(http.StatusBadRequest, gin.H{"code": http.StatusBadRequest, "message": "Invalid user ID format", "data": nil})
			return
		}
		uid := uint(id)
		userID = &uid
	}

	// Parse optional time range
	var startTime, endTime *time.Time
	timeLayout := "2006-01-02"
	if startDateStr := c.Query("start_date"); startDateStr != "" {
		t, err := time.Parse(timeLayout, startDateStr)
		if err != nil {
			c.JSON(http.StatusBadRequest, gin.H{"code": http.StatusBadRequest, "message": "Invalid start_date format, use YYYY-MM-DD", "data": nil})
			return
		}
		startTime = &t
	}
	if endDateStr := c.Query("end_date"); endDateStr != "" {
		t, err := time.Parse(timeLayout, endDateStr)
		if err != nil {
			c.JSON(http.StatusBadRequest, gin.H{"code": http.StatusBadRequest, "message": "Invalid end_date format, use YYYY-MM-DD", "data": nil})
			return
		}
		// Include the whole day
		t = t.Add(23*time.Hour + 59*time.Minute + 59*time.Second)
		endTime = &t
	}

	conversations, err := h.adminService.GetAllConversations(c.Request.Context(), userID, startTime, endTime)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"code": http.StatusInternalServerError, "message": err.Error(), "data": nil})
		return
	}

	c.JSON(http.StatusOK, gin.H{"code": http.StatusOK, "message": "success", "data": conversations})
}

// AssignOrgTagsRequest 定义了为用户分配组织标签 API 的请求体结构。
type AssignOrgTagsRequest struct {
	OrgTags []string `json:"orgTags" binding:"required"`
}

// AssignOrgTagsToUser 处理为指定用户分配组织标签的请求。
func (h *AdminHandler) AssignOrgTagsToUser(c *gin.Context) {
	var req AssignOrgTagsRequest
	// 从 URL 路径中解析 userID
	userID, err := strconv.ParseUint(c.Param("userId"), 10, 32)
	if err != nil {
		log.Warnf("AssignOrgTagsToUser: Invalid user ID format, error: %v", err)
		c.JSON(http.StatusBadRequest, gin.H{"code": http.StatusBadRequest, "message": "无效的用户 ID", "data": nil})
		return
	}

	if err := c.ShouldBindJSON(&req); err != nil {
		log.Warnf("AssignOrgTagsToUser: Invalid request payload for user ID %d, error: %v", userID, err)
		c.JSON(http.StatusBadRequest, gin.H{"code": http.StatusBadRequest, "message": "无效的请求负载", "data": nil})
		return
	}

	err = h.adminService.AssignOrgTagsToUser(uint(userID), req.OrgTags)
	if err != nil {
		log.Error("AssignOrgTagsToUser: Failed to assign tags", err)
		c.JSON(http.StatusInternalServerError, gin.H{"code": http.StatusInternalServerError, "message": "分配标签失败", "data": nil})
		return
	}

	claimsValue, _ := c.Get("claims")
	claims := claimsValue.(*token.CustomClaims)
	log.Infof("Admin user '%s' assigned tags to user ID %d", claims.Username, userID)
	c.JSON(http.StatusOK, gin.H{"code": http.StatusOK, "message": "标签分配成功", "data": nil})
}

// ListUsers 处理分页获取用户列表的请求。
func (h *AdminHandler) ListUsers(c *gin.Context) {
	page, _ := strconv.Atoi(c.DefaultQuery("page", "1"))
	size, _ := strconv.Atoi(c.DefaultQuery("size", "10"))

	userList, err := h.adminService.ListUsers(page, size)
	if err != nil {
		log.Error("ListUsers: Failed to list users", err)
		c.JSON(http.StatusInternalServerError, gin.H{"code": http.StatusInternalServerError, "message": "获取用户列表失败", "data": nil})
		return
	}

	c.JSON(http.StatusOK, gin.H{
		"code":    http.StatusOK,
		"message": "success",
		"data":    userList,
	})
}

// UpdateOrganizationTag 处理更新组织标签的请求。
func (h *AdminHandler) UpdateOrganizationTag(c *gin.Context) {
	tagID := c.Param("id")

	var req struct {
		Name        string `json:"name"`
		Description string `json:"description"`
		ParentTag   string `json:"parentTag"`
	}
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"code": http.StatusBadRequest, "message": err.Error(), "data": nil})
		return
	}

	updatedTag, err := h.adminService.UpdateOrganizationTag(tagID, req.Name, req.Description, req.ParentTag)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"code": http.StatusInternalServerError, "message": "Failed to update tag", "data": nil})
		return
	}

	c.JSON(http.StatusOK, gin.H{"code": http.StatusOK, "message": "success", "data": updatedTag})
}

// DeleteOrganizationTag 处理删除组织标签的请求。
func (h *AdminHandler) DeleteOrganizationTag(c *gin.Context) {
	tagID := c.Param("id")

	if err := h.adminService.DeleteOrganizationTag(tagID); err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"code": http.StatusInternalServerError, "message": "Failed to delete tag", "data": nil})
		return
	}

	c.JSON(http.StatusOK, gin.H{"code": http.StatusOK, "message": "Tag deleted successfully", "data": nil})
}
