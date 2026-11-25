package pyop

// 默认版本钉住：把「站点/守护用哪个运行时版本」这种选择存成插件状态目录里的小文件，
// 而不是靠改宿主 PATH（零侵入 G-31）或写系统配置。

import (
	"encoding/json"
	"os"
	"path/filepath"
	"time"
)

// PinFileName 是默认版本钉住文件名（位于插件状态目录根部）。
const PinFileName = "default-version.json"

// pinFile 是钉住记录的落盘形状。
type pinFile struct {
	Version   string    `json:"version"`
	Path      string    `json:"path"`
	BinDir    string    `json:"bin_dir"`
	UpdatedAt time.Time `json:"updated_at"`
}

// pinPath 返回钉住文件路径。
func pinPath(stateRoot string) string { return filepath.Join(filepath.Clean(stateRoot), PinFileName) }

// readPin 读取钉住记录；不存在或损坏都按「未钉住」处理。
func readPin(stateRoot string) (pinFile, bool) {
	if stateRoot == "" {
		return pinFile{}, false
	}
	data, err := os.ReadFile(pinPath(stateRoot))
	if err != nil {
		return pinFile{}, false
	}
	var pin pinFile
	if err := json.Unmarshal(data, &pin); err != nil || pin.Version == "" {
		return pinFile{}, false
	}
	return pin, true
}

// writePin 原子写钉住记录。
func writePin(stateRoot string, pin pinFile) error {
	if err := os.MkdirAll(filepath.Clean(stateRoot), GuardDirMode); err != nil {
		return err
	}
	data, err := json.MarshalIndent(pin, "", "  ")
	if err != nil {
		return err
	}
	return writeFileAtomic(pinPath(stateRoot), append(data, '\n'))
}

// clearPin 删除钉住记录；不存在视为成功（幂等）。
func clearPin(stateRoot string) error {
	err := os.Remove(pinPath(stateRoot))
	if err != nil && !os.IsNotExist(err) {
		return err
	}
	return nil
}
