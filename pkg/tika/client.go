// Package tika 提供了一个与 Apache Tika 服务器交互的客户端。
//
// ── 这个文件在整个项目里的角色 ───────────────────────────────────────────────
//
// Apache Tika 是本 RAG 系统的【文档解析器 / 文字提取工具】。
// 它是整个知识库构建流水线的第一步：把用户上传的原始二进制文件（PDF、Word、Excel 等）
// 的内容，转换成纯文本。因为向量化模型（Embedding）和大模型（LLM）都只能处理文本。
//
// ── Tika 在整个数据流中的位置 ─────────────────────────────────────────────
//
//	文件上传处理流水线（upload_service.go）完整顺序：
//
//	  ① 用户上传 PDF/Word/Excel 等文件
//	       ↓
//	  ② 文件原始二进制内容存入 MinIO（永久保存）
//	       ↓
//	  ③ 【Tika 登场】将文件内容以流的形式发送给 Tika Server
//	     → Tika 识别文件格式，自动选择合适的 Parser（如 PDFParser、DocXParser）
//	     → 提取出文件里的纯文本字符串，返回给后端
//	       ↓
//	  ④ 后端拿到纯文本后，按固定大小切分成 Chunks（文本分块）
//	       ↓
//	  ⑤ 每个 Chunk 调用 Embedding 模型向量化（pkg/embedding/client.go）
//	       ↓
//	  ⑥ 向量 + 原文 + 元数据写入 Elasticsearch（pkg/es/client.go）
//
// ── 为什么用 Tika 而不自己写 PDF/Word 解析？ ────────────────────────────
//
//   - Tika 支持超过 1000 种文件格式（PDF、DOCX、PPTX、XLS、HTML、Markdown 等）
//   - 不需要在 Go 代码里引入重量级的 PDF/Office 解析库
//   - 只需要一个运行中的 Tika Server 容器（Docker 一行启动），后端通过 HTTP 调用即可
//   - 这是微内核 + 外部服务的典型架构模式，保持了 Go 主服务的轻量
//
// ── 关键技术细节：MIME 类型 ─────────────────────────────────────────────
//
//	Tika 服务器在接收请求时，需要知道你发送的是什么类型的文件，
//	才能选择正确的 Parser 来解析。这个信息通过 HTTP 请求头 Content-Type 传递。
//	本代码中的 detectMimeType 函数，会根据文件后缀名自动推断 Content-Type，
//	例如 ".pdf" → "application/pdf"，".docx" → "application/vnd.openxmlformats-..."。
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

// Client 是 Tika 服务器的客户端，封装了与 Tika Server 的 HTTP 通信细节。
type Client struct {
	// serverURL 是 Tika Server 的 HTTP 访问地址（如 "http://localhost:9998"）
	// 由 config.yaml 中的 tika.serverURL 配置项提供
	serverURL string
}

// NewClient 是工厂函数，创建并返回一个 Tika 客户端实例。
// 由 wire 依赖注入框架在 main.go 启动时调用。
func NewClient(cfg config.TikaConfig) *Client {
	return &Client{serverURL: cfg.ServerURL}
}

// ExtractText 将文件内容（以 io.Reader 流的形式）发送给 Tika Server，
// 并返回提取出的纯文本内容。
//
// 这个函数是整个知识库构建流水线的起点，由 upload_service.go 在处理合并完整文件后调用。
//
// 工作原理：
//  1. 调用 detectMimeType 根据文件名后缀自动推断文件类型（如 PDF、DOCX）
//  2. 向 Tika Server 的 /tika 端点发送一个 HTTP PUT 请求
//     - 请求 Body = 文件的二进制内容流（直接流式传输，不缓存到内存）
//     - Content-Type Header = 文件的 MIME 类型（告诉 Tika 用哪种解析器）
//     - Accept Header = "text/plain"（告诉 Tika 只返回纯文本，不要 HTML）
//  3. 读取 Tika 的响应体，即提取出的纯文本内容
//
// 参数说明：
//   - fileReader：文件内容的数据流（可以直接传 MinIO 下载的 Object 流）
//   - fileName：文件名（用于推断 MIME 类型，如 "report.pdf"）
//
// 返回值：
//   - string：提取出的纯文本内容（之后会被切分成 Chunks 送去向量化）
func (c *Client) ExtractText(fileReader io.Reader, fileName string) (string, error) {
	// 根据文件扩展名推断 MIME 类型（告知 Tika 如何解析这个文件）
	contentType := detectMimeType(fileName)

	// 构造 HTTP PUT 请求，目标是 Tika 的 /tika 端点
	req, err := http.NewRequest("PUT", c.serverURL+"/tika", fileReader)
	if err != nil {
		return "", fmt.Errorf("创建请求失败: %w", err)
	}

	req.Header.Set("Accept", "text/plain")      // 要求 Tika 以纯文本格式返回（非 HTML）
	req.Header.Set("Content-Type", contentType) // 告知 Tika 文件格式，确保选对 Parser

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return "", fmt.Errorf("调用 Tika 失败: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		return "", fmt.Errorf("Tika 返回错误 [%d]: %s", resp.StatusCode, string(body))
	}

	// 读取 Tika 响应体，即提取出的纯文本
	buf := new(bytes.Buffer)
	if _, err := io.Copy(buf, resp.Body); err != nil {
		return "", fmt.Errorf("读取 Tika 响应失败: %w", err)
	}

	return buf.String(), nil
}

// detectMimeType 根据文件扩展名自动推断 Content-Type（MIME 类型）。
//
// ── 为什么需要正确的 MIME 类型？ ─────────────────────────────────────────
//
//	Tika Server 内置了针对不同文件格式的 Parser（解析器）：
//	 - PDF  → org.apache.tika.parser.pdf.PDFParser
//	 - DOCX → org.apache.tika.parser.microsoft.ooxml.OOXMLParser
//	 - HTML → org.apache.tika.parser.html.HtmlParser
//	当请求头 Content-Type 为 "application/pdf" 时，Tika 会自动选择 PDFParser。
//	如果给了错误的或未知的 Content-Type（如 "application/octet-stream"），
//	Tika 会尝试自动检测（但可能会更慢或失败）。
//
// 实现逻辑：
//   - 使用 Go 标准库的 mime.TypeByExtension 根据后缀名查表
//   - 如果查不到（如 ".abc" 之类的非标准后缀），返回 "application/octet-stream" 兜底
func detectMimeType(fileName string) string {
	ext := filepath.Ext(fileName)
	if ext == "" {
		return "application/octet-stream" // 没有后缀名，用通用二进制类型兜底
	}
	mimeType := mime.TypeByExtension(ext) // 如 ".pdf" → "application/pdf"
	if mimeType == "" {
		// 后缀名在标准 MIME 表中查不到，用通用类型兜底（Tika 会尝试自动检测）
		return "application/octet-stream"
	}
	return mimeType
}
