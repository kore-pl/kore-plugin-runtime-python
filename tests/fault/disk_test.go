package fault

import (
	"testing"

	"github.com/pengtang1024/kore-plugin-runtime-python/internal/pyop"
)

// TestDiskFault 注入磁盘满，期望四方法返回 E_INTERNAL（写不动宿主属于内部故障）。
func TestDiskFault(t *testing.T) {
	assertFault(t, pyop.FaultDisk, "E_INTERNAL", "磁盘满")
}

// TestDiskSanity 无故障时四方法正常返回。
func TestDiskSanity(t *testing.T) {
	sanityNoFault(t)
}
