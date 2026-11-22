package fault

import (
	"encoding/json"
	"testing"

	"github.com/pengtang1024/kore-plugin-runtime-python/internal/pyop"
)

// TestKillFault 注入 kill -9（进程被信号终止），期望 E_UPSTREAM。
func TestKillFault(t *testing.T) {
	assertFault(t, pyop.FaultKill, "E_UPSTREAM", "信号 9")
}

// TestKillFaultContract 单独验证 kill 故障的错误体结构完整（真实调用，非空壳）。
func TestKillFaultContract(t *testing.T) {
	params := json.RawMessage(`{"version":"3.11","path":"envs/app"}`)
	_, perr := pyop.Call("python.venv.create", params, testRoot, faultExecutor{kind: pyop.FaultKill})
	verifyErrorContract(t, perr)
}
