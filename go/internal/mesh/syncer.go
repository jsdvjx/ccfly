package mesh

// syncer.go — 把本地 Claude 会话数据(摘要文档 + 全文 jsonl 尾巴)周期性推到云端归档。
//
// 走云端公网控制面(st.Scheme://st.Host),凭 mesh_token 鉴权,**与 WG 隧道状态无关**
// (同 refreshConfig 的用法)——隧道断不断,同步照走。
//
// 一致性:每个会话单写者(本机)+ jsonl append-only ⇒ 同步就是幂等的 log shipping。
// 云端按会话回报的 `have` 字节数即权威高水位(HWM),故客户端**不存本地游标**:每轮拿 have、
// 用本地文件大小算差,只补送超出部分的尾巴。中断重连下一轮从 have 续传,天然断点续传。

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"log"
	"net/http"
	"net/url"
	"os"
	"strconv"
	"time"

	"github.com/jsdvjx/ccfly/go/internal/control"
)

const (
	syncInterval  = 20 * time.Second
	syncBlobChunk = 1 << 20 // 1 MiB 每次 blob 追加(云端上限 4 MiB;大尾巴自然分块续传)
)

// syncSession 是推给云端的摘要文档,json 键与云端 store.Session 对齐(注意是 "id" 不是 "session_id")。
type syncSession struct {
	ID        string `json:"id"`
	Cwd       string `json:"cwd,omitempty"`
	Title     string `json:"title,omitempty"`
	Model     string `json:"model,omitempty"`
	State     string `json:"state,omitempty"`
	Turns     int    `json:"turns"`
	Tokens    int    `json:"tokens"`
	LastTs    string `json:"last_ts,omitempty"`
	GitBranch string `json:"git_branch,omitempty"`
	Preview   string `json:"preview,omitempty"`
	AttnKind  string `json:"attn_kind,omitempty"` // 待办类型(permission/plan/choice;无则省略),见 control/attn.go
}

// runSyncer 周期性同步本地会话到云端,直到 ctx 取消。在 runTunnel 里启一次(进程级生命周期)。
func runSyncer(ctx context.Context, st *State) {
	if st.MeshToken == "" || st.Host == "" {
		return
	}
	t := time.NewTicker(syncInterval)
	defer t.Stop()
	syncOnce(ctx, st) // 连上即推一次,不等第一个 tick
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			syncOnce(ctx, st)
		}
	}
}

func syncBase(st *State) string {
	scheme := st.Scheme
	if scheme == "" {
		scheme = "https"
	}
	return scheme + "://" + st.Host
}

func syncOnce(ctx context.Context, st *State) {
	digests, err := control.SessionDigests()
	if err != nil {
		log.Printf("ccfly: sync scan failed: %v", err)
		return
	}
	have, ok := pushSummaries(ctx, st, digests)
	if !ok {
		return // 云端老版本无此端点 / 网络失败:静默跳过,下轮再试(优雅降级)
	}
	for _, d := range digests {
		if ctx.Err() != nil {
			return
		}
		if d.Size > have[d.SessionID] {
			pushBlobTail(ctx, st, d, have[d.SessionID])
		}
	}
}

// pushSummaries 批量上推摘要文档,返回云端各会话已归档的字节 HWM(have)。
func pushSummaries(ctx context.Context, st *State, digests []control.SessionDigest) (map[string]int64, bool) {
	attn := control.AttnKinds() // sid → 待办类型(memoized,见 control/attn.go);权限/选择框 jsonl 看不到
	arr := make([]syncSession, 0, len(digests))
	for _, d := range digests {
		arr = append(arr, syncSession{
			ID: d.SessionID, Cwd: d.Cwd, Title: d.Title, Model: d.Model,
			State: d.State, Turns: d.Turns, Tokens: d.Tokens, LastTs: d.LastTs,
			GitBranch: d.GitBranch, Preview: d.Preview, AttnKind: attn[d.SessionID],
		})
	}
	payload, err := json.Marshal(map[string]any{"sessions": arr})
	if err != nil {
		return nil, false
	}
	rctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(rctx, "POST",
		syncBase(st)+"/api/device/sessions?token="+url.QueryEscape(st.MeshToken), bytes.NewReader(payload))
	if err != nil {
		return nil, false
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		log.Printf("ccfly: sync push failed: %v", err)
		return nil, false
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		log.Printf("ccfly: sync push: HTTP %d", resp.StatusCode)
		return nil, false
	}
	var out struct {
		Have map[string]int64 `json:"have"`
	}
	if err := json.NewDecoder(io.LimitReader(resp.Body, 8<<20)).Decode(&out); err != nil {
		return nil, false
	}
	if out.Have == nil {
		out.Have = map[string]int64{}
	}
	return out.Have, true
}

// pushBlobTail 从 from 起把 d 的 jsonl 尾巴分块补送到云端,直到追平 d.Size。
func pushBlobTail(ctx context.Context, st *State, d control.SessionDigest, from int64) {
	f, err := os.Open(d.Path)
	if err != nil {
		return
	}
	defer f.Close()
	for from < d.Size {
		if ctx.Err() != nil {
			return
		}
		n := d.Size - from
		if n > syncBlobChunk {
			n = syncBlobChunk
		}
		buf := make([]byte, n)
		if _, err := f.ReadAt(buf, from); err != nil && err != io.EOF {
			return
		}
		next, ok := putBlob(ctx, st, d.SessionID, from, buf)
		if !ok || next <= from {
			return // 网络失败 / 云端无进展(已有或 gap):停,等下轮按新 have 重对齐
		}
		from = next
	}
}

// putBlob 追加一块尾巴,返回云端新的字节 HWM。200(已追加)与 409(gap,云端回报其当前大小)
// 都带 bytes:据此对齐;404(摘要还没上去)则本块作废,下轮先推摘要再补。
func putBlob(ctx context.Context, st *State, sid string, from int64, data []byte) (int64, bool) {
	rctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	u := syncBase(st) + "/api/device/sessions/" + url.PathEscape(sid) + "/blob" +
		"?token=" + url.QueryEscape(st.MeshToken) + "&from=" + strconv.FormatInt(from, 10)
	req, err := http.NewRequestWithContext(rctx, "PUT", u, bytes.NewReader(data))
	if err != nil {
		return 0, false
	}
	req.Header.Set("Content-Type", "application/x-ndjson")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		log.Printf("ccfly: sync blob failed: %v", err)
		return 0, false
	}
	defer resp.Body.Close()
	var out struct {
		Bytes int64 `json:"bytes"`
	}
	_ = json.NewDecoder(io.LimitReader(resp.Body, 1<<10)).Decode(&out)
	if resp.StatusCode == http.StatusOK {
		return out.Bytes, true
	}
	if resp.StatusCode == http.StatusConflict { // gap:回报的 bytes 是云端当前大小,据此续
		return out.Bytes, out.Bytes > from
	}
	log.Printf("ccfly: sync blob %.8s: HTTP %d", sid, resp.StatusCode)
	return 0, false
}
