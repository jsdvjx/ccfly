package svc

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"
)

const windowsTaskName = "ccfly"

func installWindows(o Options) error {
	if err := validate(o); err != nil {
		return err
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
		userID = name
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

	if err := copyExe(self, binPath, 0o755); err != nil {
		return fmt.Errorf("install binary: %w", err)
	}
	// Copy bundled tmux.exe (psmux) if it sits next to the source binary
	srcTmux := filepath.Join(filepath.Dir(self), "tmux.exe")
	dstTmux := filepath.Join(filepath.Dir(binPath), "tmux.exe")
	if _, e := os.Stat(srcTmux); e == nil {
		if err := copyExe(srcTmux, dstTmux, 0o755); err != nil {
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
	if o.System && !isRoot() {
		return fmt.Errorf("--system needs administrator: re-run as admin")
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
