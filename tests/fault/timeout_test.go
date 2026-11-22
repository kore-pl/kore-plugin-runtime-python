package fault

import (
	"testing"

	"github.com/pengtang1024/kore-plugin-runtime-python/internal/pyop"
)

// TestTimeoutFault 注入调用超时，期望 E_UPSTREAM。
func TestTimeoutFault(t *testing.T) {
	assertFault(t, pyop.FaultTimeout, "E_UPSTREAM", "超时")
}

// TestTimeoutSanity 无故障时方法应正常返回，防止超时测试假通过。
func TestTimeoutSanity(t *testing.T) {
	sanityNoFault(t)
}
