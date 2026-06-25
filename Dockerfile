# syntax=docker/dockerfile:1
#
# Dockerfile — 受限档(profile=restricted)的 ccfly 镜像。
#
# 这是给「受限用户类型」用的镜像:把 ccfly 的能力档在编译期 + 策略文件 + 运行用户三层
# 锁成 restricted —— 关闭代理/组网(connect/install/uninstall、overlay、向会话注入代理)、
# Claude 账号登录(ccfly claude login/logout)、以及运行期从 npm 拉新 UI(uisync)。
# 保留:serve(本地 HTTP/WS 控制面 + 内嵌 web UI)、查看/驱动已存在会话、自带网页终端 /term。
#
# 不含 Node / claude CLI(按「仅查看/驱动、不带账号」定位)。因此镜像内无法 `claude --resume`
# / 新建真实 claude 会话;/term 进入的是 bare shell。需要账号的部署请改用带 Node+claude 的变体。
#
# Web UI:ccfly 二进制 //go:embed 内嵌已构建的 SPA(go/internal/control/webdist),同源托管,
# 运行期无需外联。注意:webdist 的真实产物被 .gitignore 忽略,仅由 scripts/build-web.sh staged。
# 构建本镜像前,请确保宿主工作区已 staged(见下方护栏与 docker/README.md 的「构建前置」)。
#
# 构建:
#   bash scripts/build-web.sh          # 先把 web UI staged 进 go/internal/control/webdist(一次)
#   docker build -t ccfly:restricted . # 再构建镜像
# 运行:
#   docker run --rm -p 7699:7699 \
#     -v "$HOME/.claude:/home/app/.claude:ro" \
#     ccfly:restricted
#   # 然后浏览器打开 http://127.0.0.1:7699

############################
# 1) builder:编译「受限档」ccfly 二进制
############################
FROM golang:1.25-bookworm AS builder

ARG VERSION=docker
ARG CCFLY_PROFILE=restricted

WORKDIR /src
# 只需 go module(含已 staged 的 webdist 产物);webdist 真实文件被 .gitignore 忽略,但会随
# 构建上下文一并拷入(.dockerignore 未排除它)。下方护栏在缺失时 fail-fast。
COPY go/ ./go/
WORKDIR /src/go

# 护栏://go:embed 的 webdist 必须已含真实 SPA 产物(assets/*.js)。仓库只跟踪占位 index.html
# + VERSION;真实产物由 scripts/build-web.sh staged。缺失则直接失败并提示,避免编出空壳 UI。
RUN if ! ls internal/control/webdist/assets/*.js >/dev/null 2>&1; then \
      echo "ERROR: go/internal/control/webdist/assets 为空。" >&2; \
      echo "请先在宿主执行 'bash scripts/build-web.sh' 把 web UI staged 进去,再构建镜像。" >&2; \
      exit 1; \
    fi

# 静态编译(CGO_ENABLED=0,glibc/musl 无依赖);把能力档默认编成 restricted。
# 配合下方 /etc/ccfly/profile.json + 非 root 用户,env/文件都无法把它升回 full。
RUN CGO_ENABLED=0 go build -trimpath \
      -ldflags "-s -w -X main.version=${VERSION} -X github.com/jsdvjx/ccfly/go/internal/profile.defaultMode=${CCFLY_PROFILE}" \
      -o /out/ccfly ./cmd/ccfly

############################
# 2) runtime:最小受限镜像(不含 Node / claude CLI)
############################
FROM debian:bookworm-slim

# 运行期系统依赖:tmux(会话控制 / PTY / capture 必需)、procps(takeover 用 ps)、
# ca-certificates、locales(UTF-8,中文与 claude 框线字符需要)、tini(PID1 回收子进程)。
RUN apt-get update \
 && apt-get install -y --no-install-recommends \
      tmux procps ca-certificates locales tini \
 && echo "en_US.UTF-8 UTF-8" > /etc/locale.gen \
 && locale-gen \
 && rm -rf /var/lib/apt/lists/*

# 非 root 运行:受限用户改不动 root 拥有的策略文件、也换不掉 /usr/local/bin/ccfly —— 硬边界。
RUN useradd --create-home --uid 10001 --shell /bin/bash app

# 把「受限」能力档落成 root 拥有、只读的策略文件(与编译期 ldflags 默认双保险)。
RUN mkdir -p /etc/ccfly \
 && printf '{"mode":"restricted"}\n' > /etc/ccfly/profile.json \
 && chmod 0444 /etc/ccfly/profile.json

COPY --from=builder /out/ccfly /usr/local/bin/ccfly

# 运行期状态目录,属主归 app(挂载时 docker 会接管挂载点;不挂载则用此默认)。
RUN mkdir -p /home/app/.claude /home/app/.ccfly && chown -R app:app /home/app

ENV HOME=/home/app \
    SHELL=/bin/bash \
    LANG=en_US.UTF-8 \
    LC_ALL=en_US.UTF-8 \
    CCFLY_PORT=7699 \
    CCFLY_BIND=0.0.0.0 \
    CCFLY_ALLOW_PUBLIC_BIND=1 \
    CCFLY_PROFILE=restricted

EXPOSE 7699

# ~/.claude:已存在会话的 jsonl(建议只读挂载);~/.ccfly:panemap 等运行期状态(可写)。
VOLUME ["/home/app/.claude", "/home/app/.ccfly"]

USER app
WORKDIR /home/app

# tini 做 PID1 回收 tmux/子进程僵尸。受限档下 connect/install/claude 会 fail-closed。
ENTRYPOINT ["/usr/bin/tini", "--", "ccfly"]
CMD ["serve"]
