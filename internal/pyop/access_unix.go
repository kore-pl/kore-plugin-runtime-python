//go:build unix

package pyop

import "syscall"

// probeDirWritable 用 access(2) 真实判定目录是否可写（创建/删除条目的权限）。
// 返回 (是否可写, 是否支持探测)；不支持时调用方退回权限位启发式。
func probeDirWritable(path string) (bool, bool) {
	if err := syscall.Access(path, 0x2 /* W_OK */); err != nil {
		return false, true
	}
	return true, true
}
