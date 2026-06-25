package profile

import (
	"os"
	"path/filepath"
	"testing"
)

// withEnv 设置一个环境变量并在测试结束后还原(支持「未设置」语义)。
func withEnv(t *testing.T, key, val string) {
	t.Helper()
	prev, had := os.LookupEnv(key)
	if val == unset {
		os.Unsetenv(key)
	} else {
		os.Setenv(key, val)
	}
	t.Cleanup(func() {
		if had {
			os.Setenv(key, prev)
		} else {
			os.Unsetenv(key)
		}
	})
}

const unset = "\x00"

// allOn / anyOn 简化对「全开 / 有任一开」的断言。
func allOn(p Profile) bool {
	return p.MeshJoin && p.OverlayBridge && p.MeshProxy && p.Claude && p.Install && p.UISync
}
func anyOn(p Profile) bool {
	return p.MeshJoin || p.OverlayBridge || p.MeshProxy || p.Claude || p.Install || p.UISync
}

// writeProfileFile 在临时目录写一个策略文件,返回其路径。
func writeProfileFile(t *testing.T, body string) string {
	t.Helper()
	p := filepath.Join(t.TempDir(), "profile.json")
	if err := os.WriteFile(p, []byte(body), 0o644); err != nil {
		t.Fatalf("write profile file: %v", err)
	}
	return p
}

// setDefaultMode 临时改编译期默认(模拟 ldflags 注入),测试结束还原。
func setDefaultMode(t *testing.T, m string) {
	t.Helper()
	prev := defaultMode
	defaultMode = m
	t.Cleanup(func() { defaultMode = prev })
}

// noFile 指向一个不存在的文件,隔离测试机上可能真实存在的 /etc/ccfly/profile.json。
func noFile(t *testing.T) { withEnv(t, "CCFLY_PROFILE_FILE", filepath.Join(t.TempDir(), "nope.json")) }

func TestResolveDefaultIsFull(t *testing.T) {
	setDefaultMode(t, "full")
	withEnv(t, "CCFLY_PROFILE", unset)
	noFile(t)

	p := resolve()
	if p.Mode != ModeFull || !allOn(p) {
		t.Fatalf("default should be full + all-enabled, got %+v", p)
	}
}

func TestEnvCanDowngrade(t *testing.T) {
	setDefaultMode(t, "full")
	noFile(t)
	withEnv(t, "CCFLY_PROFILE", "restricted")

	p := resolve()
	if p.Mode != ModeRestricted || anyOn(p) {
		t.Fatalf("CCFLY_PROFILE=restricted should downgrade to all-off, got %+v", p)
	}
}

// 核心安全不变量:当默认(ldflags)已是 restricted 时,env=full 不得升回 full。
func TestEnvCannotUpgrade(t *testing.T) {
	setDefaultMode(t, "restricted")
	noFile(t)
	withEnv(t, "CCFLY_PROFILE", "full")

	if p := resolve(); p.Mode != ModeRestricted {
		t.Fatalf("env=full must NOT upgrade a restricted build, got %+v", p)
	}
}

func TestFileCanDowngrade(t *testing.T) {
	setDefaultMode(t, "full")
	withEnv(t, "CCFLY_PROFILE", unset)
	withEnv(t, "CCFLY_PROFILE_FILE", writeProfileFile(t, `{"mode":"restricted"}`))

	if p := resolve(); p.Mode != ModeRestricted {
		t.Fatalf("file mode=restricted should downgrade, got %+v", p)
	}
}

// 文件 mode=full 不得把一个 restricted 的编译默认升回 full。
func TestFileCannotUpgrade(t *testing.T) {
	setDefaultMode(t, "restricted")
	withEnv(t, "CCFLY_PROFILE", unset)
	withEnv(t, "CCFLY_PROFILE_FILE", writeProfileFile(t, `{"mode":"full"}`))

	if p := resolve(); p.Mode != ModeRestricted {
		t.Fatalf("file mode=full must NOT upgrade a restricted build, got %+v", p)
	}
}

func TestMalformedFileIgnored(t *testing.T) {
	setDefaultMode(t, "full")
	withEnv(t, "CCFLY_PROFILE", unset)
	withEnv(t, "CCFLY_PROFILE_FILE", writeProfileFile(t, `not json`))

	if p := resolve(); p.Mode != ModeFull {
		t.Fatalf("malformed file should be ignored (stay full), got %+v", p)
	}
}

func TestRestrictedBuildDefault(t *testing.T) {
	setDefaultMode(t, "restricted")
	withEnv(t, "CCFLY_PROFILE", unset)
	noFile(t)

	if p := resolve(); p.Mode != ModeRestricted || anyOn(p) {
		t.Fatalf("ldflags restricted default should yield all-off, got %+v", p)
	}
}

// instance 编译默认 → 仅 MeshJoin 开(允许受控接入),其余敏感位全关。
func TestInstanceBuildDefault(t *testing.T) {
	setDefaultMode(t, "instance")
	withEnv(t, "CCFLY_PROFILE", unset)
	noFile(t)

	p := resolve()
	if p.Mode != ModeInstance || !p.MeshJoin {
		t.Fatalf("instance default should enable MeshJoin, got %+v", p)
	}
	if p.OverlayBridge || p.MeshProxy || p.Claude || p.Install || p.UISync {
		t.Fatalf("instance must keep overlay/proxy/claude/install/uisync off, got %+v", p)
	}
}

// instance 编译默认下,env=full 不得升回 full(硬边界)。
func TestInstanceEnvCannotUpgrade(t *testing.T) {
	setDefaultMode(t, "instance")
	noFile(t)
	withEnv(t, "CCFLY_PROFILE", "full")

	if p := resolve(); p.Mode != ModeInstance {
		t.Fatalf("env=full must NOT upgrade an instance build, got %+v", p)
	}
}

// instance 可被进一步降到 restricted(可降不可升)。
func TestInstanceCanDowngradeToRestricted(t *testing.T) {
	setDefaultMode(t, "instance")
	noFile(t)
	withEnv(t, "CCFLY_PROFILE", "restricted")

	if p := resolve(); p.Mode != ModeRestricted || anyOn(p) {
		t.Fatalf("env=restricted should downgrade an instance build to all-off, got %+v", p)
	}
}

// full 默认 + env=instance → 落定 instance(env 可加严到 instance)。
func TestEnvCanSelectInstance(t *testing.T) {
	setDefaultMode(t, "full")
	noFile(t)
	withEnv(t, "CCFLY_PROFILE", "instance")

	p := resolve()
	if p.Mode != ModeInstance || !p.MeshJoin || p.OverlayBridge || p.Claude {
		t.Fatalf("env=instance should select instance (MeshJoin only), got %+v", p)
	}
}

// host 编译默认 → MeshJoin+OverlayBridge+Install 开,Claude/MeshProxy/UISync 关。
func TestHostBuildDefault(t *testing.T) {
	setDefaultMode(t, "host")
	withEnv(t, "CCFLY_PROFILE", unset)
	noFile(t)

	p := resolve()
	if p.Mode != ModeHost || !p.MeshJoin || !p.OverlayBridge || !p.Install {
		t.Fatalf("host default should enable MeshJoin+OverlayBridge+Install, got %+v", p)
	}
	if p.MeshProxy || p.Claude || p.UISync {
		t.Fatalf("host must keep proxy/claude/uisync off, got %+v", p)
	}
}

// host 编译默认下,env=full 不得升回 full(host ⊂ full,env 只能降权)。
func TestHostEnvCannotUpgrade(t *testing.T) {
	setDefaultMode(t, "host")
	noFile(t)
	withEnv(t, "CCFLY_PROFILE", "full")

	if p := resolve(); p.Mode != ModeHost {
		t.Fatalf("env=full must NOT upgrade a host build, got %+v", p)
	}
}

// instance 编译默认下,env=host 不得「拓宽」成 host(instance 比 host 严,strictest 保留 instance)。
// 即 instance 镜像不会被 env=host 解锁 OverlayBridge/Install。
func TestInstanceEnvCannotBecomeHost(t *testing.T) {
	setDefaultMode(t, "instance")
	noFile(t)
	withEnv(t, "CCFLY_PROFILE", "host")

	p := resolve()
	if p.Mode != ModeInstance || p.OverlayBridge || p.Install {
		t.Fatalf("env=host must NOT widen an instance build, got %+v", p)
	}
}
