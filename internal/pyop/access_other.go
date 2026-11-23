//go:build !unix

package pyop

// probeDirWritable 在非 unix 平台不可用，调用方退回权限位启发式。
func probeDirWritable(string) (bool, bool) { return false, false }
