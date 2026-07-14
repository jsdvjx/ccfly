package svc

import (
	"fmt"
	"os"
	"os/exec"
	"os/user"
	"path/filepath"
	"strings"
	"time"
)

const windowsTaskName = "ccfly"

func installWindows(o Options) error {
	if err := validate(o); err != nil {
		return err
	}
	// hosts 模式的硬要求:SNI arm 要写 %SystemRoot%\System32\drivers\etc\hosts,且
	// HighestAvailable 任务只有从提权上下文注册,运行时才真正拿到高 token。所以
	// Windows 上 install 一律要求管理员(安装器/快捷方式已自动提权;dry-run 放行)。
	if !o.DryRun && !isRoot() {
		return fmt.Errorf("Windows 上 install 需要管理员权限(SNI 需写 hosts、注册提权计划任务):请用管理员终端重跑,或直接用安装器 ccfly-setup(自动提权)")
	}
	name, home, _, _, err := runAs(o.System)
	if err != nil {
		return err
	}
	p := o.resolve(home)

	self, err := selfPath()
	if err != nil {
		return err
	}

	binPath := filepath.Join(home, ".ccfly", "bin", p.BinName+".exe")
	logPath := filepath.Join(home, ".ccfly", p.BinName+".log")
	taskName := windowsTaskName
	if p.LinuxUnit != linuxUnit {
		taskName = p.LinuxUnit
	}

	execArgs := strings.Join(p.Args, " ")

	// 日志重定向经 wrapper .cmd 脚本,而非把整条重定向命令塞进 cmd /c 参数:
	// cmd 的引号剥离规则(>2 个引号时剥掉首尾引号)会把
	//   /c "exe" args >> "log" 2>&1
	// 撕成无效命令,任务 Last Result=1 且连日志文件都不生成(2026-07-02 实测)。
	// wrapper 路径单对引号(恰 2 个)命中 cmd 的保留规则,含空格路径也安全。
	wrapperPath := filepath.Join(home, ".ccfly", "ccfly-task.cmd")
	wrapperBody := fmt.Sprintf("@echo off\r\n\"%s\" %s >> \"%s\" 2>&1\r\n", binPath, execArgs, logPath)
	// InteractiveToken 任务直接跑 cmd 会在用户桌面弹一个常驻黑窗(服务前台跑在里面)。
	// 经 wscript + VBS 以 windowStyle=0 隐藏启动,桌面零打扰。
	vbsPath := filepath.Join(home, ".ccfly", "ccfly-task.vbs")
	vbsBody := fmt.Sprintf("rc = CreateObject(\"Wscript.Shell\").Run(\"\"\"%s\"\"\", 0, True)\r\nWScript.Quit rc\r\n", wrapperPath)
	command := "wscript.exe"
	cmdArgs := fmt.Sprintf(`//B //Nologo "%s"`, vbsPath)

	var userID string
	if o.System {
		userID = "SYSTEM"
	} else {
		// UserId 一律用 SID,不用用户名:微软账号/AzureAD 登录的机器,调度器对用户名形式的
		// UserId **静默跳过**(任务能建、Next Run 排队,但 Last Run 永远 1999、进程从不启动;
		// DESKTOP-BN6KIGG 与 2026-07-13 两台新装 0.11.0 实测)。Go 的 user.Current().Uid 在
		// Windows 上就是 SID 字符串,本地账号同样解析,无兼容代价。
		userID = name
		if u, err := user.Current(); err == nil && strings.HasPrefix(u.Uid, "S-") {
			userID = u.Uid
		}
	}

	// 自愈触发器:除 logon/boot 外,再加「每 5 分钟重复」的日历触发 —— 服务静默死亡
	// (实测会发生:控制台事件、taskkill、崩溃)后 5 分钟内被自动拉起。IgnoreNew + connect
	// 单例锁保证已在跑时重复触发为零成本 no-op。StartBoundary 取安装当天零点(过去时刻,
	// 立即进入重复窗口)。
	repeatXML := fmt.Sprintf(`<CalendarTrigger><StartBoundary>%sT00:00:00</StartBoundary><Enabled>true</Enabled><ScheduleByDay><DaysInterval>1</DaysInterval></ScheduleByDay><Repetition><Interval>PT5M</Interval><StopAtDurationEnd>false</StopAtDurationEnd></Repetition></CalendarTrigger>`,
		time.Now().Format("2006-01-02"))
	triggerXML := `<LogonTrigger><Enabled>true</Enabled></LogonTrigger>` + repeatXML
	if o.System {
		triggerXML = `<BootTrigger><Enabled>true</Enabled></BootTrigger>` + repeatXML
	}

	taskXML := fmt.Sprintf(`<Task version="1.2" xmlns="http://schemas.microsoft.com/windows/2004/02/mit/task">
  <Triggers>
    %s
  </Triggers>
  <Principals>
    <Principal id="Author">
      <UserId>%s</UserId>
      <RunLevel>HighestAvailable</RunLevel>
      <LogonType>%s</LogonType>
    </Principal>
  </Principals>
  <Settings>
    <MultipleInstancesPolicy>IgnoreNew</MultipleInstancesPolicy>
    <DisallowStartIfOnBatteries>false</DisallowStartIfOnBatteries>
    <StopIfGoingOnBatteries>false</StopIfGoingOnBatteries>
    <AllowHardTerminate>true</AllowHardTerminate>
    <StartWhenAvailable>true</StartWhenAvailable>
    <RunOnlyIfNetworkAvailable>false</RunOnlyIfNetworkAvailable>
    <IdleSettings>
      <StopOnIdleEnd>false</StopOnIdleEnd>
      <RestartOnIdle>false</RestartOnIdle>
    </IdleSettings>
    <AllowStartOnDemand>true</AllowStartOnDemand>
    <Enabled>true</Enabled>
    <Hidden>false</Hidden>
    <RunOnlyIfIdle>false</RunOnlyIfIdle>
    <ExecutionTimeLimit>PT0S</ExecutionTimeLimit>
    <RestartOnFailure>
      <Interval>PT1M</Interval>
      <Count>999</Count>
    </RestartOnFailure>
  </Settings>
  <Actions>
    <Exec>
      <Command>%s</Command>
      <Arguments>%s</Arguments>
    </Exec>
  </Actions>
</Task>`,
		triggerXML,
		xmlEsc(userID),
		logonType(o.System),
		xmlEsc(command),
		xmlEsc(cmdArgs),
	)

	if o.DryRun {
		fmt.Printf("# bin  -> %s (copy of %s)\n# task -> %s (Task Scheduler)\n# run as %s\n\n%s\n", binPath, self, taskName, userID, taskXML)
		return nil
	}

	// 清场:杀掉其它 ccfly 进程 —— 旧实例会与新服务互顶 mesh 连接(30s 断连),
	// 且 Windows 不允许覆盖运行中的 exe(不杀则 copyExe EPERM)。
	killStaleProcesses()
	sweepOldExes(filepath.Dir(binPath))

	if err := replaceExe(self, binPath, 0o755); err != nil {
		return fmt.Errorf("install binary: %w", err)
	}
	// Copy bundled tmux.exe (psmux) if it sits next to the source binary.
	// psmux 常驻(里面跑着用户会话)且从本 bin 目录起(ensureToolPath 把它排 PATH 最前),
	// 文件被锁 → 必须走 replaceExe 的挪旧换新,直接 copyExe 覆盖会 Access is denied。
	srcTmux := filepath.Join(filepath.Dir(self), "tmux.exe")
	dstTmux := filepath.Join(filepath.Dir(binPath), "tmux.exe")
	if _, e := os.Stat(srcTmux); e == nil {
		if err := replaceExe(srcTmux, dstTmux, 0o755); err != nil {
			fmt.Fprintf(os.Stderr, "⚠ 复制 tmux.exe 失败: %v\n", err)
		} else {
			fmt.Printf("✓ tmux.exe (psmux) -> %s\n", dstTmux)
		}
	}

	ccflyDir := filepath.Join(home, ".ccfly")
	_ = os.MkdirAll(ccflyDir, 0o755)

	if err := os.WriteFile(wrapperPath, []byte(wrapperBody), 0o755); err != nil {
		return fmt.Errorf("write task wrapper: %w", err)
	}
	if err := os.WriteFile(vbsPath, []byte(vbsBody), 0o644); err != nil {
		return fmt.Errorf("write task vbs launcher: %w", err)
	}

	xmlPath := filepath.Join(ccflyDir, taskName+"-task.xml")
	if err := os.WriteFile(xmlPath, []byte(taskXML), 0o644); err != nil {
		return fmt.Errorf("write task XML: %w", err)
	}
	defer os.Remove(xmlPath)

	// Remove existing task silently (first install has no prior task)
	del := exec.Command("schtasks.exe", "/Delete", "/TN", taskName, "/F")
	del.Stdout, del.Stderr = nil, nil
	_ = del.Run()

	if err := run("schtasks.exe", "/Create", "/TN", taskName, "/XML", xmlPath); err != nil {
		return fmt.Errorf("schtasks create: %w", err)
	}

	if err := run("schtasks.exe", "/Run", "/TN", taskName); err != nil {
		fmt.Fprintf(os.Stderr, "⚠ 任务已注册但立即启动失败(将在下次登录时自动启动): %v\n", err)
	}

	fmt.Printf("✓ installed Task Scheduler task %q\n  bin: %s\n  log: %s\n  run as: %s\n  uninstall: %s uninstall%s\n",
		taskName, binPath, logPath, userID, p.BinName, systemFlag(o.System))
	return nil
}

func uninstallWindows(o Options) error {
	// 卸载同样要求管理员:除了删任务,还要清 hosts 托管块(cmd/ccfly 在 svc.Uninstall
	// 之后调 mesh.CleanupResolver)—— 服务是被硬杀的不会走 teardown,残留的块会把
	// Anthropic 域钉死在 loopback,整机 Claude 全断。
	if !o.DryRun && !isRoot() {
		return fmt.Errorf("Windows 上 uninstall 需要管理员权限(需清 hosts 托管块):请用管理员终端重跑,或走「卸载 ccfly」快捷方式 / 系统卸载入口(自动提权)")
	}
	_, home, _, _, err := runAs(o.System)
	if err != nil {
		return err
	}
	p := o.resolve(home)
	taskName := windowsTaskName
	if p.LinuxUnit != linuxUnit {
		taskName = p.LinuxUnit
	}

	if o.DryRun {
		fmt.Printf("# would delete Task Scheduler task %q\n", taskName)
		return nil
	}

	if err := run("schtasks.exe", "/End", "/TN", taskName); err != nil {
		// task might not be running
	}
	if err := run("schtasks.exe", "/Delete", "/TN", taskName, "/F"); err != nil {
		return fmt.Errorf("schtasks delete: %w", err)
	}

	fmt.Printf("✓ removed Task Scheduler task %q\n", taskName)
	return nil
}

func logonType(system bool) string {
	if system {
		return "ServiceAccount"
	}
	return "InteractiveToken"
}

// replaceExe 把 src 复制为 dst,容忍 dst 正被运行占用:Windows 不能删除/覆盖运行中的
// exe(rename 目标报 Access is denied),但**可以重命名它** —— 先把旧文件挪去 .old
// (已起的进程继续用旧镜像跑到退出,如 psmux 里用户的 Claude 会话,不打断),再落新
// 文件。.old 残留由下次安装的 sweepOldExes 清扫。
func replaceExe(src, dst string, mode os.FileMode) error {
	if _, err := os.Stat(dst); err == nil {
		aside := dst + ".old"
		_ = os.Remove(aside) // 上次遗留:没进程占着就删;删不掉(仍在跑)→ 下面换时间戳名
		if err := os.Rename(dst, aside); err != nil {
			aside = fmt.Sprintf("%s.old-%d", dst, time.Now().Unix())
			if err := os.Rename(dst, aside); err != nil {
				return err
			}
		}
	}
	return copyExe(src, dst, mode)
}

// sweepOldExes 清扫 replaceExe 留下的 *.old*(镜像仍被占用的删不掉,留给再下次)。
func sweepOldExes(dir string) {
	ents, err := os.ReadDir(dir)
	if err != nil {
		return
	}
	for _, e := range ents {
		if !e.IsDir() && strings.Contains(e.Name(), ".old") {
			_ = os.Remove(filepath.Join(dir, e.Name()))
		}
	}
}
