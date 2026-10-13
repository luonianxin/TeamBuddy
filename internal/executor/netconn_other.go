//go:build !darwin

package executor

// probeRemoteConns 在还没有套接字枚举的平台上返回未知。
// 未知必须走原来的静默判定，不能把「没查到」当成连接断开。
func probeRemoteConns(map[int]int) ModelConn {
	return ModelConnUnknown
}
