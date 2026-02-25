// Package service 包含了应用的业务逻辑层。
package service

import (
	"context"
	"errors"
	"fmt"
	"pai-smart-go/internal/model"
	"pai-smart-go/internal/repository"
	"pai-smart-go/pkg/database"
	"pai-smart-go/pkg/hash"
	"pai-smart-go/pkg/log"
	"pai-smart-go/pkg/token"
	"strings"
	"time"

	"gorm.io/gorm"
)

// UserService 接口定义了所有与用户相关的业务操作。
type UserService interface {
	Register(username, password string) (*model.User, error)
	Login(username, password string) (accessToken, refreshToken string, err error)
	GetProfile(username string) (*model.User, error)
	Logout(tokenString string) error
	SetUserPrimaryOrg(username, orgTag string) error
	GetUserOrgTags(username string) (map[string]interface{}, error)
	GetUserEffectiveOrgTags(user *model.User) ([]string, error)
	RefreshToken(refreshTokenString string) (newAccessToken, newRefreshToken string, err error)
}

// userService 是 UserService 接口的实现。
type userService struct {
	userRepo   repository.UserRepository
	orgTagRepo repository.OrgTagRepository
	jwtManager *token.JWTManager
}

// NewUserService 创建一个新的 UserService 实例。
func NewUserService(userRepo repository.UserRepository, orgTagRepo repository.OrgTagRepository, jwtManager *token.JWTManager) UserService {
	return &userService{
		userRepo:   userRepo,
		orgTagRepo: orgTagRepo,
		jwtManager: jwtManager,
	}
}

// Register 处理用户注册的业务逻辑。
func (s *userService) Register(username, password string) (*model.User, error) {
	// 1. 检查用户名是否已存在
	_, err := s.userRepo.FindByUsername(username)
	if err == nil {
		return nil, errors.New("用户名已存在")
	}
	if !errors.Is(err, gorm.ErrRecordNotFound) {
		return nil, err
	}

	// 2. 对密码进行哈希处理
	hashedPassword, err := hash.HashPassword(password)
	if err != nil {
		return nil, err
	}

	// 3. 创建新用户（不包含组织信息）
	newUser := &model.User{
		Username: username,
		Password: hashedPassword,
		Role:     "USER", // 默认角色
	}

	// 4. 将用户存入数据库以生成ID
	err = s.userRepo.Create(newUser)
	if err != nil {
		return nil, err
	}

	// 5. 创建用户的私人组织标签 (与Java逻辑对齐)
	privateTagId := "PRIVATE_" + username
	privateTagName := username + "的私人空间"

	// 检查私人标签是否已存在
	_, err = s.orgTagRepo.FindByID(privateTagId)
	if errors.Is(err, gorm.ErrRecordNotFound) {
		// 不存在则创建
		privateTag := &model.OrganizationTag{
			TagID:       privateTagId,
			Name:        privateTagName,
			Description: "用户的私人组织标签，仅用户本人可访问",
			CreatedBy:   newUser.ID, // 将创建者的ID设置为新用户自己的ID
		}
		if err := s.orgTagRepo.Create(privateTag); err != nil {
			// 此处应有回滚逻辑，但为简化，我们先记录错误
			log.Errorf("[UserService] 创建私人组织标签失败, username: %s, error: %v", username, err)
			return nil, fmt.Errorf("创建私人组织标签失败: %w", err)
		}
	} else if err != nil {
		// 处理查询时发生的其他错误
		log.Errorf("[UserService] 查询私人组织标签失败, username: %s, error: %v", username, err)
		return nil, fmt.Errorf("查询私人组织标签失败: %w", err)
	}

	// 6. 更新用户信息，为其分配私有标签
	newUser.OrgTags = privateTagId
	newUser.PrimaryOrg = privateTagId
	if err := s.userRepo.Update(newUser); err != nil {
		log.Errorf("[UserService] 更新用户组织标签失败, username: %s, error: %v", username, err)
		return nil, fmt.Errorf("更新用户组织标签失败: %w", err)
	}

	return newUser, nil
}

// Login 处理用户登录的业务逻辑。
func (s *userService) Login(username, password string) (accessToken, refreshToken string, err error) {
	// 1. 查找用户
	user, err := s.userRepo.FindByUsername(username)
	if err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return "", "", errors.New("invalid credentials")
		}
		return "", "", err
	}

	// 2. 验证密码
	if !hash.CheckPasswordHash(password, user.Password) {
		return "", "", errors.New("invalid credentials")
	}

	// 3. 生成 access token 和 refresh token
	accessToken, err = s.jwtManager.GenerateToken(user.ID, user.Username, user.Role)
	if err != nil {
		return "", "", err
	}
	refreshToken, err = s.jwtManager.GenerateRefreshToken(user.ID, user.Username, user.Role)
	if err != nil {
		return "", "", err
	}

	return accessToken, refreshToken, nil
}

// GetProfile 根据用户名获取用户详细信息。
func (s *userService) GetProfile(username string) (*model.User, error) {
	user, err := s.userRepo.FindByUsername(username)
	if err != nil {
		return nil, err
	}
	return user, nil
}

// Logout 处理用户登出逻辑，将 token 加入 Redis 黑名单。
func (s *userService) Logout(tokenString string) error {
	claims, err := s.jwtManager.VerifyToken(tokenString)
	if err != nil {
		return err
	}
	// 使用 Redis 实现一个简单的 token 黑名单。
	// token 的剩余有效期将作为 Redis key 的过期时间。
	expiration := time.Until(claims.ExpiresAt.Time)
	// 将 token 存入黑名单，值为 "true"，并设置过期时间
	return database.RDB.Set(context.Background(), "blacklist:"+tokenString, "true", expiration).Err()
}

// SetUserPrimaryOrg 设置用户的主组织。
func (s *userService) SetUserPrimaryOrg(username, orgTag string) error {
	user, err := s.userRepo.FindByUsername(username)
	if err != nil {
		return err
	}
	// 简化的验证：检查用户是否拥有该组织标签。
	// 在生产环境中，这里可能需要更复杂的逻辑来验证标签的有效性。
	if !strings.Contains(user.OrgTags, orgTag) {
		return errors.New("user does not belong to this organization")
	}
	user.PrimaryOrg = orgTag
	return s.userRepo.Update(user)
}

// GetUserOrgTags 获取用户的组织标签信息。
func (s *userService) GetUserOrgTags(username string) (map[string]interface{}, error) {
	// 这是一个简化版本。在生产环境中，可能会连接查询 organization_tags 表以获取更详细信息。
	user, err := s.userRepo.FindByUsername(username)
	if err != nil {
		return nil, err
	}

	var orgTags []string
	if user.OrgTags != "" {
		orgTags = strings.Split(user.OrgTags, ",")
	} else {
		orgTags = make([]string, 0)
	}

	var orgTagDetails []map[string]string
	if len(orgTags) > 0 {
		for _, tagID := range orgTags {
			tag, err := s.orgTagRepo.FindByID(tagID)
			if err == nil { // 忽略查找失败的标签
				tagDetail := map[string]string{
					"tagId":       tag.TagID,
					"name":        tag.Name,
					"description": tag.Description,
				}
				orgTagDetails = append(orgTagDetails, tagDetail)
			}
		}
	} else {
		orgTagDetails = make([]map[string]string, 0)
	}

	result := map[string]interface{}{
		"orgTags":       orgTags,
		"primaryOrg":    user.PrimaryOrg,
		"orgTagDetails": orgTagDetails,
	}
	return result, nil
}

// GetUserEffectiveOrgTags 获取用户实际可访问的全部组织标签（含所有祖先标签）。
//
// ── 为什么需要"向上展开"？ ───────────────────────────────────────────────
//
// 组织标签是树形结构，父标签代表更大的范围。例如：
//
//	公司（root）
//	  └── 技术部（org:tech）
//	        └── 后端组（org:tech:backend）   ← 用户 A 属于这里
//
// 如果用户 A 属于"后端组"，他理应也能看到挂在"技术部"甚至"公司"下的文档。
// 上传文档时 org_tag 可以打"技术部"，意思是"技术部所有人可见"。
// 检索时如果只用"后端组"过滤，就会漏掉这些文档。
// 所以必须把用户直接所属标签的所有父标签都展开，ES filter 才能覆盖全部可见范围。
//
// 展开前 → 展开后：
//
//	用户 OrgTags = ["org:tech:backend"]
//	有效标签   = ["org:tech:backend", "org:tech", "root"]
//
// ── 算法：BFS（广度优先搜索）向上遍历父标签 ─────────────────────────────
//
// 步骤：
//  1. 一次性拉取数据库里所有 org_tag 记录，构建 tagID -> parentTagID 的 Map（避免循环查库）
//  2. 把用户直接持有的标签作为 BFS 初始队列
//  3. 从队列取出当前标签 → 找到它的 parent → 如果 parent 没有被处理过就入队
//  4. 直到队列为空（已到达根节点或没有更多父标签）
//  5. 返回所有处理过的标签（即"能访问的完整范围"）
//
// 返回值会直接用于 search_service.go 的 ES filter：
//
//	"filter": {
//	  "bool": {
//	    "should": [
//	      {"term": {"user_id": user.ID}},           // 是自己上传的
//	      {"term": {"is_public": true}},             // 是公开文档
//	      {"terms": {"org_tag": effectiveTags}},     // ← 这里用展开后的完整标签列表
//	    ],
//	    "minimum_should_match": 1
//	  }
//	}
func (s *userService) GetUserEffectiveOrgTags(user *model.User) ([]string, error) {
	if user.OrgTags == "" {
		// 用户没有任何组织标签（理论上不会，注册时至少有私人标签）
		return []string{}, nil
	}

	// 步骤 1：一次性加载所有 org_tag，在内存里构建关系图。
	// 这样做的原因：如果在 BFS 每一步都查数据库，N 层树就要查 N 次，性能差且产生 N 次 IO。
	// 一次全量加载后，后面的父节点查找全在内存里完成，效率高。
	allTags, err := s.orgTagRepo.FindAll()
	if err != nil {
		log.Errorf("[UserService] 获取所有组织标签失败: %v", err)
		return nil, fmt.Errorf("无法获取组织标签列表: %w", err)
	}

	// 步骤 2：构建 tagID -> *parentTagID 的快速查找 Map。
	// 用指针是因为 parentTag 可能为 nil（根节点没有父标签）。
	// 如果用空字符串表示"无父节点"会和真实 tagID 冲突，用 *string 更安全。
	parentMap := make(map[string]*string)
	for _, tag := range allTags {
		parentMap[tag.TagID] = tag.ParentTag
	}

	// 步骤 3：初始化结果 Set（用 map 代替 slice，自动去重）。
	// 用 struct{} 作为 value 类型，不占额外内存（相当于 Java 的 HashSet）。
	effectiveTags := make(map[string]struct{})

	// 步骤 4：把用户直接持有的标签（逗号分隔字符串）作为 BFS 的起始节点入队。
	// 例如 user.OrgTags = "org:tech:backend,PRIVATE_alice"
	// → initialTags = ["org:tech:backend", "PRIVATE_alice"]
	initialTags := strings.Split(user.OrgTags, ",")
	queue := make([]string, 0, len(initialTags))

	for _, tagID := range initialTags {
		tagID = strings.TrimSpace(tagID) // 防止空格导致匹配失败
		if _, exists := effectiveTags[tagID]; !exists {
			effectiveTags[tagID] = struct{}{} // 加入结果集
			queue = append(queue, tagID)      // 加入 BFS 队列
		}
	}

	// 步骤 5：BFS 主循环 —— 持续向上查父标签，直到到达根节点。
	//
	// 以"后端组 → 技术部 → 公司"为例，执行过程：
	//   初始队列：["org:tech:backend"]
	//   第1轮：取出 "org:tech:backend"，父是 "org:tech" → 入队
	//   队列：["org:tech"]
	//   第2轮：取出 "org:tech"，父是 "root" → 入队
	//   队列：["root"]
	//   第3轮：取出 "root"，parentTag=nil → 什么都不做
	//   队列：[]  → 循环结束
	//   结果集：["org:tech:backend", "org:tech", "root"]
	for len(queue) > 0 {
		// 出队：取队列第一个元素（FIFO）
		currentTagID := queue[0]
		queue = queue[1:]

		// 在内存 Map 里找当前标签的父标签
		parentTagPtr, ok := parentMap[currentTagID]
		if ok && parentTagPtr != nil {
			parentTagID := *parentTagPtr
			// 父标签未被处理过 → 加入结果集并入队，继续向上找
			// 已处理过则跳过（防止循环引用导致死循环，虽然正常数据不会有环）
			if _, exists := effectiveTags[parentTagID]; !exists {
				effectiveTags[parentTagID] = struct{}{}
				queue = append(queue, parentTagID)
			}
		}
		// parentTagPtr == nil 说明到达根节点，BFS 这条路径到头了
	}

	// 步骤 6：将 map 的键转换为 string slice 返回。
	// 顺序不保证（map 遍历无序），但 ES filter 中 terms 查询不需要有序。
	result := make([]string, 0, len(effectiveTags))
	for tagID := range effectiveTags {
		result = append(result, tagID)
	}

	return result, nil
}

// RefreshToken 验证 refresh token 并签发新的 access token 和 refresh token。
func (s *userService) RefreshToken(refreshTokenString string) (newAccessToken, newRefreshToken string, err error) {
	// 1. 验证 refresh token 是否有效
	claims, err := s.jwtManager.VerifyToken(refreshTokenString)
	if err != nil {
		return "", "", errors.New("invalid refresh token")
	}

	// 2. 检查用户是否存在
	user, err := s.userRepo.FindByUsername(claims.Username)
	if err != nil {
		return "", "", errors.New("user not found")
	}

	// 3. 签发新的 token
	newAccessToken, err = s.jwtManager.GenerateToken(user.ID, user.Username, user.Role)
	if err != nil {
		return "", "", err
	}
	newRefreshToken, err = s.jwtManager.GenerateRefreshToken(user.ID, user.Username, user.Role)
	if err != nil {
		return "", "", err
	}

	return newAccessToken, newRefreshToken, nil
}
