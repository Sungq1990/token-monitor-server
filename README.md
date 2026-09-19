# Token Monitor Server

AI Coding Agent Token 用量监控 · **服务端**。接收各台电脑上 [token-monitor-client](../token-monitor-client) 的上报，用 SQLite 存储，提供统计接口和网页面板。

```text
设备 A 客户端 ─┐
设备 B 客户端 ─┼─ HTTP 上报 ─► 本服务（Go + SQLite） ─► 浏览器面板 http://<服务器>:8765/
设备 C 客户端 ─┘
```

- 单个二进制，无外部依赖；数据只有一个 SQLite 文件。
- 面板**没有登录**，直接打开就能看。请只在内网 / 可信网络部署。
- 采集频率、Agent 路径、模型单价都在各设备的客户端里设置（配置存服务端，服务端面板本身无设置项）。

## 一键启动（Docker）

```bash
git clone <repo> token-monitor-server
cd token-monitor-server
docker compose up -d --build
```

打开 http://127.0.0.1:8765/ 。数据保存在 `./data/token-monitor.db`（compose 挂载的 volume），删掉容器不会丢数据。

改端口：编辑 `docker-compose.yml` 的 `ports`（如 `"9000:8765"`）。
时区：容器内 `TZ=Asia/Shanghai`，按天统计以此为准，按需修改。

```bash
docker compose logs -f          # 看日志
docker compose down             # 停止
docker compose up -d --build    # 更新代码后重建
```

## 不用 Docker 直接跑

需要 Go 1.22+（纯 Go SQLite 驱动，不需要 gcc）：

```bash
go mod tidy          # 首次拉取依赖并生成 go.sum
go build -o token-monitor-server .
./token-monitor-server -addr :8765 -db data/token-monitor.db
```

环境变量 `LISTEN_ADDR`、`DB_PATH` 与上述参数等价。国内网络拉依赖慢可先 `export GOPROXY=https://goproxy.cn,direct`（Docker 构建则取消 Dockerfile 里对应注释）。

## 接入客户端

在每台要统计的电脑上安装客户端（配置以服务端为准：除服务端地址和 device_id 两个引导字段外，全部配置保存在服务端 `devices.config_json`，客户端启动时自动拉取），设置页填服务端地址（例如 `http://192.168.1.10:8765`），保存后客户端会自动注册设备并按设定频率上报。面板的「设备」区会列出所有设备，可以按设备筛选用量。

设备唯一标识（device_id）由客户端首次启动时生成并持久化在本机；同一台机器重装客户端如果想沿用历史数据，在客户端设置页把 device_id 改回原值即可。

## API

客户端接口：

```text
POST /api/v1/devices/register   {device_id, name, os, hostname, client_version, interval_minutes, agents, status}
POST /api/v1/devices/heartbeat  同上
POST /api/v1/ingest             {device_id, agent, batches:[{source, items:[UsageItem]}]}
GET  /api/v1/pricing            → {pricing:[...]}
PUT  /api/v1/pricing            {device_id, items:[{agent, model, input_per_m, output_per_m, cache_read_per_m, cache_write_per_m}]}
```

面板接口（所有统计接口支持 `device_id=&agent=&model=&start=&end=` 过滤）：

```text
GET    /api/health
GET    /api/devices
DELETE /api/devices/{device_id}         删除设备及其全部数据
GET    /api/pricing  /  POST /api/pricing
GET    /api/stats/today
GET    /api/stats/range?bucket=auto|hour|day|month
GET    /api/stats/agents
GET    /api/stats/models
GET    /api/stats/devices
GET    /api/stats/sessions?limit=&offset=
GET    /api/usage?session_id=&limit=&offset=
GET    /api/session/{agent}/{session_id}?device_id=
```

`UsageItem` 字段：`message_id, session_id, model, provider, input_tokens, output_tokens, cache_read_tokens, cache_write_tokens, reasoning_tokens, total_tokens, cost, occurred_at, session_title, session_started_at, session_last_message_at`。时间为 RFC3339。

去重键 `(device_id, agent, message_id)`，重复上报只覆盖不累加。

## 数据表

| 表 | 用途 |
| --- | --- |
| `devices` | 设备信息、最近心跳、客户端上报的 Agent 配置快照 |
| `agent_sessions` | 会话（device_id + agent + session_id 唯一） |
| `token_usage` | 每次模型请求的真实用量（device_id + agent + message_id 唯一） |
| `model_pricing` | agent + model 的单价（¥ / 百万 token），全局生效 |

费用口径与原单机版一致：配置了单价的模型按单价重算，没配置的沿用 Agent 自报 cost。

## 备份

```bash
docker compose exec token-monitor sh -c 'cp /data/token-monitor.db /data/backup-$(date +%F).db'
```

或直接复制宿主机 `./data/token-monitor.db`（WAL 模式下建议先停服务或用 sqlite3 `.backup`）。
