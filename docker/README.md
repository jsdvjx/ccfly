# ccfly 镜像 + cc.hn 托管编排

ccfly 支持「云端为普通用户托管实例」的完整链路:运营方在云端有多核 VM(如 **top.pm**),
cc.hn 收到创建请求后经 overlay 指挥 VM 上的 host-agent `docker run` 一个实例容器;容器作为
**受控 mesh 节点**接入 overlay;终端用户带访问令牌经 cc.hn 反代访问自己的实例。

## 四个组件

| 组件 | 在哪 | 作用 | 能力档 |
| --- | --- | --- | --- |
| **实例镜像** `ccfly:instance` | 容器(VM 上) | 跑 Claude Code;`ccfly connect` 接入 overlay | `instance` |
| **host-agent** `ccfly-hostd` | VM(top.pm) | 接入 overlay,受 cloud 经 overlay 指挥 `docker run`/`rm` | `host` |
| **cloud 编排** | ccfly-cloud | provision API:建实例身份→选 host→下发 spawn;令牌反代 `/x/` | — |
| **查看器镜像**(可选) | 任意 | 纯受限,只查看/驱动已存在会话(见仓库根 `Dockerfile`) | `restricted` |

## 数据流

```
运营方后台 ──Bearer PROVISION_TOKEN──▶ cc.hn  POST /api/provision/instances {env:{ANTHROPIC_API_KEY...}}
  cloud:建实例device+连接码 → 预占 overlay IP → 签 access_token(存hash) → 选在线 host(top.pm)
       → 经 overlay dial top.pm 的 host-agent:7700  POST /spawn
  host-agent(top.pm): docker run -d ccfly:instance  (--env-file 注入 CCFLY_CONNECT_TARGET + 用户 env)
  容器: ccfly connect cc.hn/<码> → 独立 mesh 节点入网 + 自动起一个 claude 会话
  cloud 返回 {device_id, access_token}
终端用户 ──access_token──▶ cc.hn/x/{device_id}/...  → 令牌鉴权 → 复用现有 gateway 反代到容器:7699
```

## 能力档(全序子集 `full ⊃ host ⊃ instance ⊃ restricted`)

| 位 | full | host | instance | restricted |
| --- | :-: | :-: | :-: | :-: |
| MeshJoin(connect 接入) | ✅ | ✅ | ✅ | ❌ |
| OverlayBridge(端口转发) | ✅ | ✅ | ❌ | ❌ |
| Install(常驻服务) | ✅ | ✅ | ❌ | ❌ |
| MeshProxy(注代理 env) | ✅ | ❌ | ❌ | ❌ |
| Claude(账号登录) | ✅ | ❌ | ❌ | ❌ |
| UISync(npm 拉 UI) | ✅ | ❌ | ❌ | ❌ |

档位来源「最严格者胜、env 只能降权」:ldflags 默认 → `/etc/ccfly/profile.json` → `CCFLY_PROFILE`。
npm 分发的二进制不注入 = `full`,现有用户零影响。

---

## 部署(以 top.pm 为例)

### 1. 构建实例镜像

```sh
bash scripts/build-web.sh                                   # staged web UI(需 ../ccfly-ttyd-ui)
docker build -f docker/Dockerfile.instance -t ccfly:instance .
# 在 top.pm 上构建,或推到 registry 让 top.pm 拉。镜像默认 profile=instance。
```

### 2. 配置 cloud(ccfly-cloud)

```sh
CCFLY_CLOUD_PROVISION_TOKEN=<强随机>     # 运营方后台调 provision API 的密钥(为空则整组 404)
CCFLY_CLOUD_HOST_AGENT_TOKEN=<强随机>    # cloud→host-agent 的 Bearer(须与 top.pm 上一致)
CCFLY_CLOUD_PROVISION_IMAGE=ccfly:instance   # 默认
CCFLY_CLOUD_HOST_AGENT_PORT=7700             # 默认,须与 ccfly-hostd 一致
```

### 3. 在 top.pm 上装 host-agent

```sh
# (a) 在 cloud 建一个 host,拿连接码:
curl -X POST -H "Authorization: Bearer $PROVISION_TOKEN" \
     https://cc.hn/api/provision/hosts -d '{"label":"top.pm"}'
#  → {"connect":"cc.hn/<码>", "host_id":"...", "connect_code":"<码>"}

# (b) 用 host 档构建 ccfly-hostd(不在 npm 分发矩阵,运营方自建):
go -C go build -ldflags "-X github.com/jsdvjx/ccfly/go/internal/profile.defaultMode=host" \
   -o ccfly-hostd ./cmd/ccfly-hostd

# (c) 在 top.pm 上安装常驻(须已装 docker,且运行用户能访问 docker daemon):
CCFLY_HOST_AGENT_TOKEN=<同 cloud 的 HOST_AGENT_TOKEN> ./ccfly-hostd install cc.hn/<码> --system
```

host-agent 接入后在自己的 overlay IP:7700 上提供 spawn API,**网络层只放行 cloud 网关 100.64.0.1**
(overlay expose 源白名单)+ 应用层 Bearer 令牌双层鉴权。

### 4. 运营方为用户创建实例

```sh
curl -X POST -H "Authorization: Bearer $PROVISION_TOKEN" \
     https://cc.hn/api/provision/instances \
     -d '{"label":"alice","env":{"ANTHROPIC_API_KEY":"sk-ant-..."}}'
#  → {"device_id":"...", "access_token":"i....", "overlay_ip":"...", "host_id":"...", "container_id":"..."}
```

认证 env 三选一(白名单透传进容器):`ANTHROPIC_API_KEY` / 网关 `ANTHROPIC_BASE_URL`+`ANTHROPIC_AUTH_TOKEN` /
Bedrock·Vertex(`CLAUDE_CODE_USE_BEDROCK`/`CLAUDE_CODE_USE_VERTEX`+`AWS_*`/`CLOUD_ML_REGION`…)。
实例行为可调:`CCFLY_WORKSPACE`/`CCFLY_AUTOSTART`/`CCFLY_SKIP_PERMISSIONS`/`CCFLY_PERMISSION_MODE`。

### 5. 终端用户访问

把 `access_token` 嵌入你自己的产品分发给终端用户:

```
https://cc.hn/x/<device_id>/...
  HTTP:  Authorization: Bearer <access_token>
  SSE / WebSocket(/term 等无法带 header):  ?access_token=<access_token>
```

### 6. 销毁实例

```sh
curl -X DELETE -H "Authorization: Bearer $PROVISION_TOKEN" \
     https://cc.hn/api/provision/instances/<device_id>
#  cloud 去 host docker rm 容器 + 删设备 + 回收 overlay IP
```

---

## 安全

- **受限闸门是护栏**:instance 档禁 overlay 端口转发 / 账号登录 / 常驻服务 / UI 同步;用户自带任意 env
  也**无法**用 `CCFLY_TMUX_PROXY` 重开代理(被忽略)或 `CCFLY_PROFILE` 升档(env 只能降权)。
- **host-agent spawn API**:网络层 overlay 源白名单(仅 100.64.0.1)+ 应用层 Bearer 令牌。
- **凭证取舍(知情)**:用户 `ANTHROPIC_*` key 以明文经 cloud→host 的 overlay(WG 加密信道)下发,
  **cloud 必然经手明文**(与 `claude login` 流「cloud 永不见明文」不同,属本架构有意降级)。缓解:
  provision/host token 严格保密、env 绝不入日志、`access_token` 只存 hash(删实例/重签即吊销)、
  host VM(top.pm)视为受信、spawn 用 `--env-file`(凭证不进宿主 `docker inspect`/`ps`)。
- **每实例独立状态**:每个容器各自 `ccfly connect` 落各自的 `~/.ccfly/conn-*.json`;host-agent 每容器
  用独立卷(默认匿名卷),**切勿** bind-mount 共享宿主 `.ccfly`,否则多容器设备身份/overlay IP 串号。

## 本机已验证 / 待真机验证

- ✅ 设备端 Go:profile 四档 + 各闸门 + `ccfly-hostd`/`hostagent` + 镜像改 connect —— `go build`/`vet`/`test` +
  ldflags 端到端(instance 档:connect 放行、overlay-forward/claude/install 被拒、env 升不回)全过。
- ✅ cloud Go:config/auth/schema/store/provision/hostspawn/gateway —— `go build`/`vet` + 起服务 curl
  验证 provision 鉴权(401/201/503)+ 编排前段 + **回滚**(失败实例已删,仅留 host)。
- ⚠️ **未在本机验证(需 Linux + docker)**:`docker build`(本机无 docker);cloud WG datapath + gateway 反代 +
  令牌鉴权(WG kernel hub 仅 Linux,mac `s.wg==nil` → /x/ 返回 503);真实 host-agent `docker run` 全链路。
  这些请在 Linux cloud / top.pm 上跑通。
