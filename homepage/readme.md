# 派聪明 (PaiSmart) - 首页

## 项目简介

派聪明是一款基于 Java 技术栈的 RAG（检索增强生成）知识库项目。HomePage为该项目提供了一个现代化的首页展示，采用响应式设计，支持深色/浅色主题切换，为用户提供优秀的浏览体验。

## 功能特性

### 🎨 界面特性

- **响应式设计**：完美适配桌面端、平板和移动端
- **主题切换**：支持深色/浅色主题模式，自动保存用户偏好
- **现代化 UI**：采用 Tailwind CSS 构建，界面简洁美观
- **动画效果**：集成 GSAP 动画库，提供流畅的页面交互体验
- **粒子背景**：动态粒子效果，增强视觉吸引力

### 🚀 核心功能

- **项目展示**：展示派聪明 RAG 知识库的核心功能
- **技术栈展示**：展示项目使用的技术栈（MySQL、Redis、MinIO、Kafka、Elasticsearch）
- **AI 模型支持**：展示支持的多种 AI 模型（DeepSeek、通义千问、OpenAI、Claude 等）
- **功能流程**：详细展示 RAG 知识库的完整工作流程
- **社区链接**：提供微信社群、知识星球等社区入口

## 技术栈

### 前端技术

- **HTML5**：语义化标签，良好的 SEO 支持
- **CSS3**：Tailwind CSS 框架，快速样式开发
- **JavaScript (ES6+)**：现代 JavaScript 语法
- **GSAP**：专业级动画库
- **Typed.js**：打字机效果
- **Particles.js**：粒子效果库

### 设计系统

- **Tailwind CSS**：原子化 CSS 框架
- **Bootstrap Icons**：图标库
- **响应式断点**：移动优先的设计理念

## 文件结构

```
homepage/
├── index.html              # 主页面文件
├── index.js                # 主要 JavaScript 逻辑
├── readme.md               # 项目说明文档
├── package.json            # 项目依赖配置
├── assets/                 # 静态资源目录
│   ├── logo/              # Logo 图片
│   ├── images/            # 图片资源
│   │   ├── brand-logos/   # 品牌 Logo
│   │   ├── home/          # 首页图片
│   │   ├── icon/          # 图标文件
│   │   └── people/        # 人物图片
│   └── svg-icon/          # SVG 图标
├── css/                   # 样式文件
│   ├── index.css          # 自定义样式
│   ├── tailwind.min.css   # Tailwind CSS
│   └── bootstrap-icons.min.css # Bootstrap 图标
├── fonts/                 # 字体文件
├── public/                # 公共资源
│   ├── css/
│   ├── fonts/
│   └── js/                # 第三方 JS 库
└── scripts/               # 脚本文件
    ├── components.js      # 组件脚本
    └── particles.js       # 粒子效果脚本
```

## 使用说明

### 本地开发

1. **克隆项目**

   ```bash
   git clone https://github.com/itwanger/PaiSmart.git
   cd PaiSmart/homepage
   ```

2. **安装依赖**

   ```bash
   pnpm i
   ```

3. **启动本地服务器**

   ```bash
   pnpm run tw:dev # 编译tailwindcss开发环境代码
   pnpm run dev # 启动开发服务器
   ```

4. **部署**

   ```bash
   pnpm run tw:build # 编译tailwindcss生产环境代码
   pnpm run build # 构建项目
   pnpm run preview # 本地预览
   # 复制dist文件夹，发布到服务器
   ```

## 自定义配置

### 主题配置

主题相关的 CSS 变量定义在 `css/index.css` 中：

```css
:root {
    --primary-color: #646cff;
    --secondary-color: #c258f5;
    /* 更多变量... */
}
```

### 动画配置

GSAP 动画配置在 `index.js` 中：

```javascript
gsap.to(".reveal-up", {
    opacity: 0,
    y: "100%",
})
```

### 粒子效果配置

粒子效果配置在 `scripts/particles.js` 中：

```javascript
particlesJS('particles-js', {
    // 粒子配置...
})
```

## 浏览器兼容性

- **现代浏览器**：Chrome 80+、Firefox 75+、Safari 13+、Edge 80+
- **移动端**：iOS Safari 13+、Chrome Mobile 80+
- **不支持**：IE 11 及以下版本

## 联系方式

- **项目主页**：<https://github.com/itwanger/PaiSmart>
- **作者**：沉默王二
- **微信**：qing_gee
- **知识星球**：<https://javabetter.cn/zhishixingqiu/>

---

**注意**：本文档会随着项目的发展持续更新，请定期查看最新版本。
