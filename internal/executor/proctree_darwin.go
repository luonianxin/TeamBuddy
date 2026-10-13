//go:build darwin

package executor

import (
	"context"
	"os/exec"
	"strconv"
	"strings"
	"syscall"
	"time"

	"golang.org/x/sys/unix"
)

func startInTreePlatform(cmd *exec.Cmd) (procTree, error) {
	CreateProcessGroup(cmd)
	if err := cmd.Start(); err != nil {
		return nil, err
	}
	return &unixProcTree{pid: cmd.Process.Pid}, nil
}

type unixProcTree struct {
	pid int
}

func (t *unixProcTree) PID() int { return t.pid }

func (t *unixProcTree) Close() error { return nil }

func (t *unixProcTree) Sample() (ProcSample, error) {
	sample, err := sampleProcessTable()
	if err != nil {
		return ProcSample{IOBytes: -1, Readable: false}, err
	}
	pids := descendantsOf(sample.parents, t.pid)
	// 根进程退出后孙进程会被 launchd 收养（ppid=1），只按父子关系会漏掉。
	// 进程组 id 不随收养改变，按 pgid 补齐。
	for pid, pgid := range sample.pgids {
		if pgid == t.pid {
			pids[pid] = sample.parents[pid]
		}
	}
	var cpu time.Duration
	if _, ok := sample.parents[t.pid]; ok {
		pids[t.pid] = sample.parents[t.pid]
	}
	for pid := range pids {
		cpu += sample.cpu[pid]
	}
	return ProcSample{PIDs: pids, CPU: cpu, IOBytes: -1, Readable: true}, nil
}

func (t *unixProcTree) Empty() bool {
	sample, err := t.Sample()
	if err != nil || !sample.Readable {
		return false
	}
	// 采样已排除僵尸进程：根进程退出后在 cmd.Wait 回收前一直是僵尸，
	// 不能因此把进程树当成非空。
	return len(sample.PIDs) == 0
}

// Terminate 先 SIGTERM 整个进程组，再 SIGKILL。
// 不用 waitGroupGone：根进程在 cmd.Wait 之前是僵尸，kill(-pgid, 0) 一直成功，
// 会让每轮正常结束后的回收白等 grace+killWait。
func (t *unixProcTree) Terminate(grace time.Duration) error {
	if grace <= 0 {
		grace = terminateGracePeriod
	}
	if t.pid <= 0 || t.Empty() {
		return nil
	}
	_ = syscall.Kill(-t.pid, syscall.SIGTERM)
	if t.waitEmpty(grace) {
		return nil
	}
	if err := syscall.Kill(-t.pid, syscall.SIGKILL); err != nil && err != syscall.ESRCH {
		return err
	}
	t.waitEmpty(terminateKillWait)
	return nil
}

func (t *unixProcTree) waitEmpty(grace time.Duration) bool {
	deadline := time.Now().Add(grace)
	for {
		if syscall.Kill(-t.pid, 0) == syscall.ESRCH || t.Empty() {
			return true
		}
		if time.Now().After(deadline) {
			return false
		}
		time.Sleep(200 * time.Millisecond)
	}
}

// rootGone 报告根进程是否已退出。僵尸（已退出、未被 Wait 回收）算已退出，
// 否则 kill(pid, 0) 会一直成功，RootExited 永远不触发。
func rootGone(pid int) bool {
	if pid <= 0 {
		return true
	}
	info, err := unix.SysctlKinfoProc("kern.proc.pid", pid)
	if err != nil || info == nil || info.Proc.P_pid == 0 {
		return !pidAlive(pid)
	}
	return info.Proc.P_stat == 5 // SZOMB
}

type processTable struct {
	parents map[int]int
	pgids   map[int]int
	cpu     map[int]time.Duration
}

func sampleProcessTable() (processTable, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	out, err := exec.CommandContext(ctx, "ps", "-axo", "pid=,ppid=,pgid=,stat=,time=").Output()
	if err != nil {
		return processTable{}, err
	}
	table := processTable{parents: map[int]int{}, pgids: map[int]int{}, cpu: map[int]time.Duration{}}
	for _, line := range strings.Split(string(out), "\n") {
		fields := strings.Fields(line)
		if len(fields) < 5 || strings.HasPrefix(fields[3], "Z") {
			continue
		}
		pid, errPID := strconv.Atoi(fields[0])
		ppid, errPPID := strconv.Atoi(fields[1])
		pgid, errPGID := strconv.Atoi(fields[2])
		if errPID != nil || errPPID != nil || errPGID != nil || pid <= 0 {
			continue
		}
		table.parents[pid] = ppid
		table.pgids[pid] = pgid
		table.cpu[pid] = parsePSTime(strings.Join(fields[4:], ""))
	}
	return table, nil
}

func descendantsOf(parents map[int]int, root int) map[int]int {
	out := map[int]int{}
	if root <= 0 || len(parents) == 0 {
		return out
	}
	children := map[int][]int{}
	for pid, ppid := range parents {
		children[ppid] = append(children[ppid], pid)
	}
	stack := append([]int{}, children[root]...)
	seen := map[int]bool{}
	for len(stack) > 0 {
		pid := stack[len(stack)-1]
		stack = stack[:len(stack)-1]
		if pid == root || seen[pid] {
			continue
		}
		seen[pid] = true
		out[pid] = parents[pid]
		stack = append(stack, children[pid]...)
	}
	return out
}

// parsePSTime 解析 ps 的累计 CPU 时间。macOS 常见形式是 m:ss 或 h:mm:ss。
func parsePSTime(value string) time.Duration {
	value = strings.TrimSpace(value)
	if value == "" {
		return 0
	}
	days := 0
	if dash := strings.IndexByte(value, '-'); dash >= 0 {
		parsed, err := strconv.Atoi(value[:dash])
		if err != nil {
			return 0
		}
		days = parsed
		value = value[dash+1:]
	}
	parts := strings.Split(value, ":")
	nums := make([]int, len(parts))
	var frac time.Duration
	for i, part := range parts {
		part = strings.TrimSpace(part)
		if dot := strings.IndexByte(part, '.'); dot >= 0 {
			// macOS 输出 m:ss.hh；保留小数，否则 200ms 的 CPU 阈值按整秒才会跳变。
			if i == len(parts)-1 {
				if f, err := strconv.ParseFloat("0"+part[dot:], 64); err == nil {
					frac = time.Duration(f * float64(time.Second))
				}
			}
			part = part[:dot]
		}
		n, err := strconv.Atoi(part)
		if err != nil {
			return 0
		}
		nums[i] = n
	}
	var hours, mins, secs int
	switch len(nums) {
	case 1:
		secs = nums[0]
	case 2:
		mins, secs = nums[0], nums[1]
	default:
		hours, mins, secs = nums[0], nums[1], nums[2]
	}
	return time.Duration(days*24+hours)*time.Hour + time.Duration(mins)*time.Minute + time.Duration(secs)*time.Second + frac
}
