#!/usr/bin/env bash
# Token Monitor 服务端部署/升级脚本（无需 docker compose）
# 用法：先 docker load -i token-monitor-server.tar，bash deploy.sh
# 数据目录默认 /mnt/data_mmcblk1p4/docker_data/token_monitor（可用 DATA_DIR=xxx 覆盖）
set -euo pipefail
cd "$(dirname "$0")"

DATA_DIR="${DATA_DIR:-/mnt/data_mmcblk1p4/docker_data/token_monitor}"
PORT="${PORT:-8765}"

mkdir -p "$DATA_DIR"
# 若 data 目录是空的且旁边有 token-monitor.db 备份，自动就位
if [ ! -f "$DATA_DIR/token-monitor.db" ] && [ -f "$PWD/token-monitor.db" ]; then
  cp "$PWD/token-monitor.db" "$DATA_DIR/token-monitor.db"
  echo "已导入备份数据库 -> $DATA_DIR/token-monitor.db"
fi

docker rm -f token-monitor 2>/dev/null || true
docker run -d --name token-monitor \
  --restart unless-stopped \
  -p "$PORT:8765" \
  -e LISTEN_ADDR=":8765" \
  -e DB_PATH=/data/token-monitor.db \
  -e TZ=Asia/Shanghai \
  -v "$DATA_DIR:/data" \
  token-monitor-server:latest

sleep 1
echo "--- 健康检查:"
curl -s "http://127.0.0.1:$PORT/api/health" || echo "（本机 curl 不通，试试从外部访问 http://<服务器IP>:$PORT/）"
