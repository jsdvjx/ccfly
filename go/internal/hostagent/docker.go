package hostagent

// docker.go — host-agent 对 docker CLI 的薄封装(os/exec,不引 docker Go SDK,保持小静态二进制)。
// VM 上必然已装 docker,CLI 必在 PATH。

import (
	"fmt"
	"os"
	"os/exec"
	"sort"
	"strings"
)

type docker struct{ bin string }

// run 起一个 detached 实例容器。用 --env-file(0600 临时文件)传 env,避免用户凭证出现在
// 宿主 `docker inspect` / `ps` 的命令行里。容器名 + label 便于后续精确 stop / 列举。
func (d *docker) run(name, image string, env map[string]string) (string, error) {
	envFile, err := writeEnvFile(env)
	if err != nil {
		return "", err
	}
	defer os.Remove(envFile)
	args := []string{
		"run", "-d",
		"--name", name,
		"--restart", "unless-stopped",
		"--label", "ccfly.managed=1",
		"--label", "ccfly.name=" + name,
		"--env-file", envFile,
		image,
	}
	out, err := exec.Command(d.bin, args...).CombinedOutput()
	if err != nil {
		return "", fmt.Errorf("docker run: %v: %s", err, strings.TrimSpace(string(out)))
	}
	return firstLine(string(out)), nil // docker run -d 回一行容器 id
}

// stop 强制删除容器(stop + rm)。幂等:容器不存在也按成功(忽略 "No such container")。
func (d *docker) stop(name string) error {
	out, err := exec.Command(d.bin, "rm", "-f", name).CombinedOutput()
	if err != nil {
		msg := strings.TrimSpace(string(out))
		if strings.Contains(msg, "No such container") {
			return nil
		}
		return fmt.Errorf("docker rm -f %s: %v: %s", name, err, msg)
	}
	return nil
}

// clearData 清空一个实例的用户数据但保留容器与 ~/.ccfly/conn-* 接入身份。
// 先确保容器在跑,再在容器内以 app 用户结束 Claude/tmux 并清理受控目录;最后 restart
// 让 entrypoint 重建 onboarding 状态和首个干净会话。命令本身不拼接 name,避免 shell 注入。
func (d *docker) clearData(name string) error {
	if out, err := exec.Command(d.bin, "start", name).CombinedOutput(); err != nil {
		return fmt.Errorf("docker start %s: %v: %s", name, err, strings.TrimSpace(string(out)))
	}
	const clear = `set -eu
tmux kill-server >/dev/null 2>&1 || true
find /home/app/workspace -mindepth 1 -delete
find /home/app/.claude -mindepth 1 -delete
rm -f /home/app/.claude.json /home/app/.ccfly/panemap.json
rm -rf /home/app/.ccfly/uploads`
	if out, err := exec.Command(d.bin, "exec", "-u", "app", name, "sh", "-c", clear).CombinedOutput(); err != nil {
		return fmt.Errorf("docker exec clear-data %s: %v: %s", name, err, strings.TrimSpace(string(out)))
	}
	if out, err := exec.Command(d.bin, "restart", name).CombinedOutput(); err != nil {
		return fmt.Errorf("docker restart %s: %v: %s", name, err, strings.TrimSpace(string(out)))
	}
	return nil
}

// list 返回本机由 ccfly 托管的容器(name<TAB>status,逐行)。
func (d *docker) list() (string, error) {
	out, err := exec.Command(d.bin, "ps", "-a",
		"--filter", "label=ccfly.managed=1",
		"--format", "{{.Names}}\t{{.Status}}").CombinedOutput()
	if err != nil {
		return "", fmt.Errorf("docker ps: %v: %s", err, strings.TrimSpace(string(out)))
	}
	return strings.TrimSpace(string(out)), nil
}

// writeEnvFile 把 env 写成 docker --env-file 格式(KEY=VAL 每行)到一个 0600 临时文件,返回路径。
func writeEnvFile(env map[string]string) (string, error) {
	f, err := os.CreateTemp("", "ccfly-env-*.env")
	if err != nil {
		return "", err
	}
	defer f.Close()
	_ = f.Chmod(0o600)
	keys := make([]string, 0, len(env))
	for k := range env {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	var b strings.Builder
	for _, k := range keys {
		// --env-file 一行一个 KEY=VAL;值里的换行会破坏格式,剔除之。
		b.WriteString(k)
		b.WriteByte('=')
		b.WriteString(strings.ReplaceAll(env[k], "\n", ""))
		b.WriteByte('\n')
	}
	if _, err := f.WriteString(b.String()); err != nil {
		return "", err
	}
	return f.Name(), nil
}

func firstLine(s string) string {
	s = strings.TrimSpace(s)
	if i := strings.IndexByte(s, '\n'); i >= 0 {
		return s[:i]
	}
	return s
}
