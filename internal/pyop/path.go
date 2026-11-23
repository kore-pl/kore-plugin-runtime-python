package pyop

import (
	"os"
	"path/filepath"
	"strings"

	"github.com/pengtang1024/kore/pkg/pluginapi"
)

// 默认白名单根目录：站点目录、可选软件目录、用户目录，加上插件状态目录（<state>）。
var defaultAllowedRoots = []string{"/var/www", "/opt", "/home"}

// allowedRoots 返回给定状态目录下的完整白名单（去重、保留声明顺序）。
func allowedRoots(stateRoot string) []string {
	roots := make([]string, 0, len(defaultAllowedRoots)+1)
	seen := map[string]bool{}
	for _, root := range defaultAllowedRoots {
		roots = append(roots, root)
		seen[root] = true
	}
	if state := strings.TrimSpace(stateRoot); state != "" {
		if clean := filepath.Clean(state); filepath.IsAbs(clean) && !seen[clean] {
			roots = append(roots, clean)
		}
	}
	return roots
}

// ValidateRoot 校验 root 参数并返回解析后的真实绝对路径。
//
// 两层校验：
//  1. 词法层：必须是非空绝对路径、不含 NUL / 反斜杠 / ".." 段，且落在白名单目录内；
//  2. 真实层：ResolveExisting（EvalSymlinks + 存在性 + 必须是目录）后再次比对归属，
//     挡住「白名单目录本身或父级被换成软链」的逃逸。
func ValidateRoot(raw, stateRoot string) (string, *pluginapi.Error) {
	trimmed := strings.TrimSpace(raw)
	if trimmed == "" {
		return "", invalid("root 不能为空")
	}
	if trimmed != raw {
		return "", invalid("root 不得含首尾空白")
	}
	if strings.ContainsRune(trimmed, 0) {
		return "", invalid("root 含 NUL 字节")
	}
	if strings.Contains(trimmed, "\\") {
		return "", invalid("root 含反斜杠")
	}
	if !filepath.IsAbs(trimmed) {
		return "", invalid("root 必须是绝对路径")
	}
	for _, segment := range strings.Split(trimmed, "/") {
		if segment == ".." {
			return "", invalid("root 含 .. 段，禁止穿越")
		}
	}
	clean := filepath.Clean(trimmed)
	roots := allowedRoots(stateRoot)
	if !withinAny(roots, clean) {
		return "", invalid("root 不在允许目录内（/var/www、/opt、/home 或插件状态目录）")
	}
	real, perr := resolveExistingDir(clean, "root")
	if perr != nil {
		return "", perr
	}
	realRoots := make([]string, 0, len(roots))
	for _, root := range roots {
		if candidate, err := filepath.EvalSymlinks(root); err == nil {
			if info, statErr := os.Stat(candidate); statErr == nil && info.IsDir() {
				realRoots = append(realRoots, candidate)
			}
		}
	}
	if !withinAny(realRoots, real) {
		return "", invalid("root 经符号链接解析后越出允许目录")
	}
	return real, nil
}

// resolveExistingDir 解析必须已存在的目录：跟随符号链接并确认目标是目录。
func resolveExistingDir(path, field string) (string, *pluginapi.Error) {
	real, err := filepath.EvalSymlinks(path)
	if err != nil {
		if os.IsNotExist(err) {
			return "", invalid(field + " 不存在或不可访问")
		}
		return "", invalid(field + " 解析失败: " + err.Error())
	}
	info, err := os.Stat(real)
	if err != nil {
		return "", invalid(field + " 不存在或不可访问")
	}
	if !info.IsDir() {
		return "", invalid(field + " 必须是目录")
	}
	return real, nil
}

// within 判定 p 是否等于 root 或位于 root 之下（基于 Rel，避免字符串前缀误判）。
func within(root, p string) bool {
	rel, err := filepath.Rel(root, p)
	if err != nil {
		return false
	}
	return rel == "." || (rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator)))
}

// withinAny 判定 p 是否落在任意一个白名单根目录内。
func withinAny(roots []string, p string) bool {
	for _, root := range roots {
		if within(root, p) {
			return true
		}
	}
	return false
}
