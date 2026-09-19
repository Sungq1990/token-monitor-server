# ---- build ----
FROM golang:1.22-alpine AS build
WORKDIR /src
ENV CGO_ENABLED=0 GOFLAGS=-mod=mod
# 国内环境构建慢时可打开：
# ENV GOPROXY=https://goproxy.cn,direct
COPY go.mod go.sum* ./
RUN go mod download
COPY . .
RUN go build -trimpath -ldflags="-s -w" -o /out/token-monitor-server .

# ---- runtime ----
FROM alpine:3.20
RUN apk add --no-cache tzdata ca-certificates
WORKDIR /app
COPY --from=build /out/token-monitor-server /app/token-monitor-server
ENV LISTEN_ADDR=:8765 DB_PATH=/data/token-monitor.db
VOLUME ["/data"]
EXPOSE 8765
ENTRYPOINT ["/app/token-monitor-server"]
