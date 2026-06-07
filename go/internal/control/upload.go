package control

// upload.go — POST /upload:把表世界(web/移动端)选/拍/粘贴/拖拽的图片或文件落盘到
// **会话 cwd 下的 .ccfly-uploads/**,返回绝对路径,供前端把路径并进下一条提交(里世界
// Claude 据路径读图/读文件)。纯本地落盘,不向任何云端上报。
//
// 安全模型(本文件是「不可信上传」的整道闸,逐条对应评审标记的攻击面):
//   - 体积闸 BEFORE 解析:r.Body 先包 http.MaxBytesReader,超限在读 multipart 时即 413,
//     杜绝「先把无界 body 读进内存再判大小」的内存耗尽(MaxBytesReader 触发后 ParseMultipartForm
//     返回错,统一 413)。上限取 env CCFLY_MAX_UPLOAD_BYTES,缺省 32MiB。
//   - 内存阈值可配:ParseMultipartForm 的内存阈值取 env CCFLY_UPLOAD_MEM_BYTES(缺省 8MiB,低于
//     标准库 32MiB 默认),小于此存内存、超出溢出临时文件;溢出文件无论成败都由 RemoveAll() 清掉。
//   - 整体超时:挂 env CCFLY_UPLOAD_TIMEOUT_SEC(缺省 120s)的上下文,慢速 body(Slowloris 类)
//     不能无限占连接/临时文件;超时则读 body/落盘报错、temp 被清。
//   - 落盘位置**只由服务端定**:cwd 取自 `session` QUERY 参数解析出的会话冻结 cwd(同 /sendkeys
//     的 resolveSessionParam 口径,扛 /clear)。**完全忽略**客户端传来的任何 path/session_id 字段
//     —— 那是评审点名的路径穿越向量;客户端无权指定落盘目录。
//   - 文件名**服务端生成**:crypto/rand 16 字节 hex(碰撞概率可忽略,且不泄露原名),再接一个
//     从原文件名派生、白名单 [a-z0-9] 过滤后的扩展名(去前导点、小写;无合法扩展名 → .bin)。
//     **绝不**用客户端文件名当路径的任何部分。
//   - 即便名字是我们自己生成的,仍做穿越终检(抗符号链接 + 跨平台):先 EvalSymlinks(dir) 解开
//     沿途所有 link 取真实物理目录,再用 filepath.Rel 判 final 真在该目录之内(拒 ".."/绝对路径)。
//     不再用 strings.HasPrefix 字符串前缀比对 —— 那看不穿 symlink、且在大小写不敏感 FS(macOS/Win)误判。
//   - 原子落盘:同目录建 temp → io.Copy → fsync → chmod 0644 → rename 到 final。任一步失败
//     500 且删 temp(绝不留半截文件;rename 在同一文件系统内原子,读者要么看不到、要么看到完整文件)。
//
// 鉴权(关键部署约束):本端点**自身不鉴权**。它把「不可信上传」直接落盘到会话 cwd,故
// **必须**只部署在上游反代(cloud gateway 的 requireAuth + 设备归属校验)之后,**绝不**直接对外暴露:
//   - 缺省绑回环(127.0.0.1:7699),仅 cloud gateway 经 WG overlay 反代进来;
//   - 若把 control 服务绑到非回环地址(直接暴露设备端口),任何能到达该端口的人都能无鉴权上传 ——
//     这是单点失效面。Serve() 在绑非回环且未显式 opt-in(env CCFLY_ALLOW_PUBLIC_BIND=1)时会打 WARN
//     提醒运维确认确实在可信网络/反代之后。
//   - 纵深防御预留:如需设备侧二次校验,可在反代注入共享密钥头由本端点核验(当前未启用,留作扩展)。

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"
)

// defaultMaxUploadBytes 是单次上传体积上限的缺省值(32MiB)。可被 env CCFLY_MAX_UPLOAD_BYTES 覆盖。
const defaultMaxUploadBytes int64 = 32 << 20

// defaultUploadMemBytes 是 ParseMultipartForm 的内存阈值缺省值(8MiB):小于此存内存、
// 超出溢出到临时文件(标准库语义)。刻意低于 32MiB 标准默认,缩小单请求内存占用、限制并发 DoS 面;
// 可被 env CCFLY_UPLOAD_MEM_BYTES 覆盖。
const defaultUploadMemBytes int64 = 8 << 20

// defaultUploadTimeout 是单次上传整体处理超时(读 body + 落盘)。慢速 body(Slowloris 类)
// 不该无限占着连接/临时文件,故给上下文截止;可被 env CCFLY_UPLOAD_TIMEOUT_SEC(秒)覆盖。
const defaultUploadTimeout = 120 * time.Second

// maxUploadBytes 取上传体积上限:env CCFLY_MAX_UPLOAD_BYTES(正整数字节)优先,否则缺省 32MiB。
// 非法/非正值一律回落缺省(env 写错不应放开闸门或锁死上传)。
func maxUploadBytes() int64 {
	if v := os.Getenv("CCFLY_MAX_UPLOAD_BYTES"); v != "" {
		if n, err := strconv.ParseInt(v, 10, 64); err == nil && n > 0 {
			return n
		}
	}
	return defaultMaxUploadBytes
}

// uploadMemBytes 取 multipart 内存阈值:env CCFLY_UPLOAD_MEM_BYTES(正整数字节)优先,否则缺省 8MiB。
// 非法/非正值回落缺省(降低单请求内存占用、限制并发临时文件 DoS 面)。
func uploadMemBytes() int64 {
	if v := os.Getenv("CCFLY_UPLOAD_MEM_BYTES"); v != "" {
		if n, err := strconv.ParseInt(v, 10, 64); err == nil && n > 0 {
			return n
		}
	}
	return defaultUploadMemBytes
}

// uploadTimeout 取单次上传整体超时:env CCFLY_UPLOAD_TIMEOUT_SEC(正整数秒)优先,否则缺省 120s。
// 非法/非正值回落缺省(防慢速 body 长期占连接/临时文件)。
func uploadTimeout() time.Duration {
	if v := os.Getenv("CCFLY_UPLOAD_TIMEOUT_SEC"); v != "" {
		if n, err := strconv.ParseInt(v, 10, 64); err == nil && n > 0 {
			return time.Duration(n) * time.Second
		}
	}
	return defaultUploadTimeout
}

// uploadDirForSession 解析「该把文件落到哪个目录」——只信服务端解析出的会话 cwd,绝不信客户端。
//   - 主路径:session(tmux 名)→ resolveSessionParam 扛 /clear → 反查 sid → sidCwd 取冻结 cwd
//     → <cwd>/.ccfly-uploads/。
//   - 兜底:cwd 取不到(无该会话 / 非 cc- 名 / 扫盘失败)→ ~/.ccfly/uploads/(仍是服务端定的安全目录)。
//
// 注:cwd 是会话**首个** jsonl 事件冻结的初始 cwd(与 resume 作用域一致,见 claudescan.go),
// 不取 tmux pane 的实时 cwd —— /clear 后会话搬家但 cwd 语义应保持初始,与全局口径对齐。
func uploadDirForSession(session string) string {
	if s := strings.TrimSpace(session); s != "" {
		// 与 /sendkeys 同口径:把前端传来的 tmux 名(可能因 /clear 而陈旧)解析到真 pane 名,
		// 再反查 sid 取其冻结 cwd。任一环节缺数据则落兜底目录。
		resolved := resolveSessionParam(s)
		if snaps, err := scanClaudeSessions(); err == nil && len(snaps) > 0 {
			if sid := sidForTmuxName(resolved, snaps); sid != "" {
				if cwd := sidCwd(sid, snaps); cwd != "" {
					return filepath.Join(cwd, ".ccfly-uploads")
				}
			}
		}
	}
	// 兜底:~/.ccfly/uploads/(cwd 不可知时仍落在服务端掌控的目录,绝不用客户端路径)。
	home, _ := os.UserHomeDir()
	return filepath.Join(home, ".ccfly", "uploads")
}

// sanitizeExt 从客户端原文件名派生一个**安全的**扩展名:取最后一个点后的片段,小写,
// 仅保留 [a-z0-9](剥掉点、空格、路径分隔、unicode 等一切其它字符),非空则前缀点返回。
// 没有合法扩展名(无点 / 过滤后空)→ ".bin"。
// 这样既保住「让 Claude 按扩展名认图(.png/.jpg…)」的实用性,又彻底切断「靠原名注入路径/控制字符」。
func sanitizeExt(filename string) string {
	ext := filepath.Ext(filename) // 含前导点;无点则 ""
	ext = strings.ToLower(strings.TrimPrefix(ext, "."))
	var b strings.Builder
	for _, r := range ext {
		if (r >= 'a' && r <= 'z') || (r >= '0' && r <= '9') {
			b.WriteRune(r)
		}
	}
	clean := b.String()
	// 扩展名长度封顶:超长扩展(如 1000 个 a)在部分文件系统会触发路径长度限制;统一退回 .bin。
	if clean == "" || len(clean) > 16 {
		return ".bin"
	}
	return "." + clean
}

// randomName 生成服务端文件名:16 字节 crypto/rand 的 hex(32 hex 字符)+ 安全扩展名。
// crypto/rand 失败(极罕见,系统熵源故障)→ 返回空串,调用方据此 500(绝不退化成可预测名)。
func randomName(ext string) string {
	buf := make([]byte, 16)
	if _, err := rand.Read(buf); err != nil {
		return ""
	}
	return hex.EncodeToString(buf) + ext
}

// handleUpload — POST /upload?session=<tmux名>,multipart/form-data,必填表单字段 file。
// 成功:200 {path:<绝对最终路径>, size:<字节数>, name:<服务端生成的展示名>}。
// 失败:400(无 file / 坏或空 multipart body / 穿越终检失败)、413(超体积上限)、500(落盘/熵源失败)。
// 400 与 413 已精确区分(见下),便于区别「上游误丢 body」与「文件过大」两类症状。
func handleUpload(w http.ResponseWriter, r *http.Request) {
	// 整体超时(防慢速 body 长期占连接/临时文件):给请求挂一个截止上下文,落盘各步与读 body 都受其约束。
	ctx, cancel := context.WithTimeout(r.Context(), uploadTimeout())
	defer cancel()
	r = r.WithContext(ctx)

	max := maxUploadBytes()
	// 体积闸必须在**解析之前**:把 body 包成 MaxBytesReader,超限会在下面读 multipart 时报错。
	r.Body = http.MaxBytesReader(w, r.Body, max)
	// 内存阈值(可配,缺省 8MiB,见 uploadMemBytes):小于此存内存、超出溢出到临时文件(标准库语义)。
	// 超过 max 的总 body 会被 MaxBytesReader 在此处截断报错 → 我们统一回 413。
	// 注:无论成败,下面 r.MultipartForm.RemoveAll() 都会清掉标准库可能落的溢出临时文件(defer 保证)。
	if err := r.ParseMultipartForm(uploadMemBytes()); err != nil {
		// 精确区分两类失败(评审点名:别把「空 body/坏 multipart」与「超大」混成一个含糊错):
		//   - *http.MaxBytesError → 确系超过体积上限,回 413「file too large」,前端冒「文件过大」。
		//   - 其它(空 body / 缺 boundary / 截断的 multipart)→ 400「bad/empty multipart body」,
		//     这正是上游反代若误丢 body 时的症状,明确告知便于排障(对应评审的「multipart proxy path loss」)。
		var maxErr *http.MaxBytesError
		if errors.As(err, &maxErr) {
			ctrlJSON(w, http.StatusRequestEntityTooLarge, map[string]any{"error": "file too large (max " + strconv.FormatInt(max, 10) + " bytes)"})
			return
		}
		ctrlErr(w, 400, "bad or empty multipart body: "+err.Error())
		return
	}
	if r.MultipartForm != nil {
		defer r.MultipartForm.RemoveAll() // 清掉标准库可能落的溢出临时文件
	}

	// 只认表单字段 'file';忽略任何 path/session_id 等客户端字段(评审标记的穿越向量)。
	file, hdr, err := r.FormFile("file")
	if err != nil {
		ctrlErr(w, 400, "missing form field 'file'")
		return
	}
	defer file.Close()

	dir := uploadDirForSession(r.URL.Query().Get("session"))
	if err := os.MkdirAll(dir, 0o755); err != nil {
		ctrlErr(w, 500, "mkdir: "+err.Error())
		return
	}

	name := randomName(sanitizeExt(hdr.Filename))
	if name == "" {
		ctrlErr(w, 500, "name gen failed") // crypto/rand 熵源故障:绝不退化成可预测名
		return
	}
	// 穿越终检(纵深防御 + 抗符号链接 + 跨平台安全):名字虽是我们自己生成的(不含分隔符),
	// 仍核 final 真实落在 dir 之内。旧版用 strings.HasPrefix 字符串前缀比对,有两处不安全:
	//   1) 符号链接:攻击者可在 .ccfly-uploads/ 下预置 symlink(如 evil -> /etc)逃逸,字符串前缀
	//      比对看不穿 link 的真实目标。
	//   2) 大小写不敏感 FS(macOS/Windows):字符串前缀比对对 Foo/ 与 foo/ 误判。
	// 改用:对 dir 做 EvalSymlinks 取真实物理路径(解开沿途所有 link),再用 filepath.Rel 判定
	// final 是否真在该物理目录之内(Rel 结果不得为 ".." 或以 ".."+sep 开头、也不得是绝对路径)。
	// EvalSymlinks 要求路径存在 —— 此处 dir 刚 MkdirAll 过,故可解析;失败一律 400(拒可疑路径)。
	realDir, err := filepath.EvalSymlinks(dir)
	if err != nil {
		ctrlErr(w, 400, "bad target dir")
		return
	}
	cleanDir := filepath.Clean(realDir)
	final := filepath.Clean(filepath.Join(cleanDir, name))
	rel, err := filepath.Rel(cleanDir, final)
	if err != nil || rel == ".." || strings.HasPrefix(rel, ".."+string(os.PathSeparator)) || filepath.IsAbs(rel) {
		ctrlErr(w, 400, "bad target path")
		return
	}

	// 原子落盘:同目录建 temp → 拷贝(MaxBytesReader 仍在 file 链路上,逐块拷也受体积闸约束)
	// → fsync → chmod 0644 → rename。任一步失败 500 且删 temp(绝不留半截文件)。
	tmp, err := os.CreateTemp(cleanDir, ".ccfly-up-*")
	if err != nil {
		ctrlErr(w, 500, "temp: "+err.Error())
		return
	}
	tmpPath := tmp.Name()
	// cleanup 关闭并删除 temp;仅在 rename 成功后置 nil 不再触发(rename 后 temp 名已消失)。
	cleanup := func() {
		tmp.Close()
		os.Remove(tmpPath)
	}
	size, err := io.Copy(tmp, file)
	if err != nil {
		cleanup()
		// io.Copy 期间撞上 MaxBytesReader 上限 → 体积超限,回 413;其它拷贝错回 500。
		if strings.Contains(err.Error(), "http: request body too large") {
			ctrlJSON(w, http.StatusRequestEntityTooLarge, map[string]any{"error": "file too large (max " + strconv.FormatInt(max, 10) + " bytes)"})
			return
		}
		ctrlErr(w, 500, "write: "+err.Error())
		return
	}
	if err := tmp.Sync(); err != nil { // fsync:确保字节真正落盘后再 rename(防崩溃后空文件)
		cleanup()
		ctrlErr(w, 500, "fsync: "+err.Error())
		return
	}
	if err := tmp.Close(); err != nil {
		os.Remove(tmpPath)
		ctrlErr(w, 500, "close: "+err.Error())
		return
	}
	if err := os.Chmod(tmpPath, 0o644); err != nil { // 上传文件 0644:owner 读写、其它只读(Claude 能读)
		os.Remove(tmpPath)
		ctrlErr(w, 500, "chmod: "+err.Error())
		return
	}
	if err := os.Rename(tmpPath, final); err != nil { // 同 FS 原子换名:读者要么看不到、要么看到完整文件
		os.Remove(tmpPath)
		ctrlErr(w, 500, "rename: "+err.Error())
		return
	}

	ctrlJSON(w, 200, map[string]any{"path": final, "size": size, "name": name})
}
