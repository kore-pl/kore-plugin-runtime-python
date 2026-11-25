//go:build windows

package pyop

// Windows 暂不支持单进程守护（Tier B 降级：能力缺失显式降级，禁止静默失败）。
// 降级不静默：guard.* 方法返回 E_UNSUPPORTED 并带「怎么办」提示，列表仍可读。

// NewSystemGuardIO 返回不支持平台的操作面。
func NewSystemGuardIO() GuardIO { return unsupportedGuardIO{} }

// unsupportedGuardIO 的全部进程操作都返回 ErrGuardPlatform。
type unsupportedGuardIO struct{}

func (unsupportedGuardIO) Spawn(SpawnSpec) (SpawnResult, error) {
	return SpawnResult{}, ErrGuardPlatform
}

func (unsupportedGuardIO) Signal(int, bool) error { return ErrGuardPlatform }

func (unsupportedGuardIO) Probe(int) (ProcInfo, bool) { return ProcInfo{}, false }
