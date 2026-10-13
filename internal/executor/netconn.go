package executor

// ModelConn 是进程树对外 TCP 连接的观察结果。
// 只在「模型请求已经开始」之后用来区分还在等响应，还是连接已经没了。
type ModelConn int

const (
	// ModelConnUnknown 表示这次没能列出套接字。调用方必须退回静默判定，不能当成断线。
	ModelConnUnknown ModelConn = iota
	// ModelConnUp 表示进程树里还有已建立的 TCP 连接。
	ModelConnUp
	// ModelConnDown 表示套接字列表成功，且没有任何已建立的 TCP 连接。
	ModelConnDown
)

func remoteConnsOpen(pids map[int]int) ModelConn {
	return probeRemoteConns(pids)
}
