#!/usr/bin/env bash
# ccfly-instance — 「普通用户实例」镜像的容器入口(instance 档)。
#
# 职责:
#   1. 起 ccfly connect <CCFLY_CONNECT_TARGET> 作为 PID1 —— 既接入 cc.hn overlay(被 Hub 反代),
#      又进程内起 control 服务(固定在 CCFLY_LOCAL_PORT)。它持有用户经 --env-file 注入的环境
#      (ANTHROPIC_API_KEY 等),首次拉起的 tmux server 据此继承,之后所有会话都继承到凭证 → claude 可认证。
#   2. control ready 后,走 ccfly 自己的 POST /new 自动起一个 Claude Code 会话(可关):
#      /new 会预信任工作目录(跳过「信任此文件夹」对话框)、按需注入 IS_SANDBOX、规范成
#      cc-<sid8> 命名并登记 panemap —— 比裸 `tmux ... claude` 稳。
#
# 可调环境变量:
#   CCFLY_CONNECT_TARGET    接入目标 cc.hn 或 cc.hn/<连接码>(host-agent 下发;默认 cc.hn)
#   CCFLY_LOCAL_PORT        进程内 control 端口(默认 7699;entrypoint 据此探活 + POST /new)
#   CCFLY_WORKSPACE         默认会话的工作目录(default /home/app/workspace)
#   CCFLY_AUTOSTART         1=自动起会话(默认);0=不自动,留给用户在网页里新建
#   CCFLY_SKIP_PERMISSIONS  1/true=给 claude --dangerously-skip-permissions(默认 false)
#   CCFLY_PERMISSION_MODE   传给 claude 的 --permission-mode(可选)
set -euo pipefail

PORT="${CCFLY_LOCAL_PORT:-7699}"
WORKSPACE="${CCFLY_WORKSPACE:-/home/app/workspace}"
mkdir -p "$WORKSPACE" 2>/dev/null || true

# 规范化布尔:1/true/yes/on → true,其余 → false。
norm_bool() {
  case "$(printf '%s' "${1:-}" | tr '[:upper:]' '[:lower:]')" in
    1 | true | yes | on) echo true ;;
    *) echo false ;;
  esac
}

# 凭证自检(仅警告,不致命):用户必须自带其一,否则会话里的 claude 无法认证。
if [ -z "${ANTHROPIC_API_KEY:-}" ] && [ -z "${ANTHROPIC_AUTH_TOKEN:-}" ] &&
  [ -z "${CLAUDE_CODE_USE_BEDROCK:-}" ] && [ -z "${CLAUDE_CODE_USE_VERTEX:-}" ]; then
  echo "ccfly-instance: 警告 — 未检测到 Claude 凭证环境变量。" >&2
  echo "  请用 docker -e 注入其一:ANTHROPIC_API_KEY;或 ANTHROPIC_BASE_URL + ANTHROPIC_AUTH_TOKEN(自带网关);或 Bedrock/Vertex。" >&2
  echo "  serve 仍会启动,但会话里的 claude 会因未认证而无法工作。" >&2
fi

# 自动起一个会话:等 serve ready,再 POST /new(后台进行,不阻塞 serve 启动)。
if [ "$(norm_bool "${CCFLY_AUTOSTART:-1}")" = "true" ]; then
  (
    skip="$(norm_bool "${CCFLY_SKIP_PERMISSIONS:-}")"
    body="{\"cwd\":\"${WORKSPACE}\",\"skip_permissions\":${skip}"
    if [ -n "${CCFLY_PERMISSION_MODE:-}" ]; then
      body="${body},\"permission_mode\":\"${CCFLY_PERMISSION_MODE}\""
    fi
    body="${body}}"
    for _ in $(seq 1 60); do
      if curl -sf "http://127.0.0.1:${PORT}/healthz" >/dev/null 2>&1; then
        if curl -sf -X POST "http://127.0.0.1:${PORT}/new" \
          -H 'content-type: application/json' -d "$body" >/dev/null 2>&1; then
          echo "ccfly-instance: 已在 ${WORKSPACE} 自动起一个 Claude Code 会话" >&2
        else
          echo "ccfly-instance: 自动起会话失败(serve 已在跑,可在网页里手动新建)" >&2
        fi
        exit 0
      fi
      sleep 1
    done
    echo "ccfly-instance: 等待 serve ready 超时,跳过自动起会话" >&2
  ) &
fi

# connect 作为前台进程(tini 的子进程,正确收信号):接入 overlay + 进程内 control(固定 CCFLY_LOCAL_PORT)。
exec ccfly connect "${CCFLY_CONNECT_TARGET:-cc.hn}"
