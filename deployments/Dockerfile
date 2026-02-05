# --- Build Stage ---
# 使用官方 Go 镜像作为构建环境
FROM golang:1.22-alpine AS builder

# 设置工作目录
WORKDIR /app

# 复制 go.mod 和 go.sum 文件并下载依赖
# 这一步利用了 Docker 的层缓存机制，只有在依赖变化时才会重新下载
COPY go.mod go.sum ./
RUN go mod download

# 复制所有源代码
COPY . .

# 编译应用
# CGO_ENABLED=0 禁用了 CGO，使得编译出的二进制文件是静态链接的，不依赖于任何 C 库
# -o /app/server 指定输出的二进制文件名为 server，并放在 /app 目录下
RUN CGO_ENABLED=0 GOOS=linux go build -o /app/server ./cmd/server

# --- Final Stage ---
# 使用一个非常轻量级的 Alpine Linux 镜像作为最终的运行环境
FROM alpine:latest

# 设置工作目录
WORKDIR /app

# 从 builder 阶段复制编译好的二进制文件
COPY --from=builder /app/server /app/server

# 复制配置文件
# 注意：docker-compose 中需要将本地的 configs 目录挂载到容器中
# 这里复制一份是为了在没有挂载的情况下也能提供默认配置
COPY configs /app/configs

# 暴露应用端口
EXPOSE 8080

# 容器启动时执行的命令
ENTRYPOINT ["/app/server"]
