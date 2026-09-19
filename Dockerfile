# ---- build ----
# 构建阶段固定跑在构建机原生架构上，交叉编译出目标架构（TARGETARCH）的二进制，无需 QEMU 模拟。
# tzdata / ca-certificates 也在此阶段安装（数据文件与架构无关），运行阶段只做 COPY，无需模拟。
FROM --platform=$BUILDPLATFORM golang:1.22-alpine AS build
WORKDIR /src
ARG TARGETOS
ARG TARGETARCH
ENV CGO_ENABLED=0 GOFLAGS=-mod=mod
# 国内环境构建慢时可打开：
ENV GOPROXY=https://goproxy.cn,direct
RUN apk add --no-cache tzdata ca-certificates
COPY go.mod go.sum* ./
RUN go mod download
COPY . .
RUN GOOS=${TARGETOS:-linux} GOARCH=${TARGETARCH:-amd64} \
    go build -trimpath -ldflags="-s -w" -o /out/token-monitor-server .

# ---- runtime ----
# 注意：本阶段没有任何 RUN，因此可以在异架构（如 arm64）上构建而无需 QEMU
FROM alpine:3.20
COPY --from=build /usr/share/zoneinfo /usr/share/zoneinfo
COPY --from=build /etc/ssl/certs/ca-certificates.crt /etc/ssl/certs/
WORKDIR /app
COPY --from=build /out/token-monitor-server /app/token-monitor-server
ENV LISTEN_ADDR=:8765 DB_PATH=/data/token-monitor.db
VOLUME ["/data"]
EXPOSE 8765
ENTRYPOINT ["/app/token-monitor-server"]
