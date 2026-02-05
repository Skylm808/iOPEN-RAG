// Package tika 提供了一个与 Apache Tika 服务器交互的客户端。
package tika

import (
	"bytes"
	"fmt"
	"io"
	"mime"
	"net/http"
	"pai-smart-go/internal/config"
	"path/filepath"
)

// Client 是 Tika 服务器的客户端。
type Client struct {
	serverURL string
}

// NewClient 创建一个新的 Tika 客户端实例。
func NewClient(cfg config.TikaConfig) *Client {
	return &Client{serverURL: cfg.ServerURL}
}

// ExtractText 自动根据文件后缀推断 MIME 类型，并调用 Tika 提取文本。
func (c *Client) ExtractText(fileReader io.Reader, fileName string) (string, error) {
	// 自动根据文件名推断 MIME 类型
	contentType := detectMimeType(fileName)

	req, err := http.NewRequest("PUT", c.serverURL+"/tika", fileReader)
	if err != nil {
		return "", fmt.Errorf("创建请求失败: %w", err)
	}

	req.Header.Set("Accept", "text/plain")
	req.Header.Set("Content-Type", contentType)

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return "", fmt.Errorf("调用 Tika 失败: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		return "", fmt.Errorf("Tika 返回错误 [%d]: %s", resp.StatusCode, string(body))
	}

	buf := new(bytes.Buffer)
	if _, err := io.Copy(buf, resp.Body); err != nil {
		return "", fmt.Errorf("读取 Tika 响应失败: %w", err)
	}

	return buf.String(), nil
}

// detectMimeType 根据文件扩展名判断 Content-Type
func detectMimeType(fileName string) string {
	ext := filepath.Ext(fileName)
	if ext == "" {
		return "application/octet-stream"
	}
	mimeType := mime.TypeByExtension(ext)
	if mimeType == "" {
		// fallback 默认
		return "application/octet-stream"
	}
	return mimeType
}
