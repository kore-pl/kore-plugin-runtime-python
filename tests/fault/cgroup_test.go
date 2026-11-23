package fault

import (
	"testing"

	"github.com/pengtang1024/kore-plugin-runtime-python/internal/pyop"
)

// TestCgroupFault 注入 cgroup 限额，期望四方法返回 E_UNSUPPORTED（当前实现未提供限额管理）。
func TestCgroupFault(t *testing.T) {
	assertFault(t, pyop.FaultCgroup, "E_UNSUPPORTED", "cgroup")
}

// TestCgroupSanity 无故障时四方法正常返回。
func TestCgroupSanity(t *testing.T) {
	sanityNoFault(t)
}
