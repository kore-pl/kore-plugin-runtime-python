package pyop

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/pengtang1024/kore/pkg/pluginapi"
)

// TestValidateRootStateRoot 状态目录本身及其子目录是合法 root，返回真实绝对路径。
func TestValidateRootStateRoot(t *testing.T) {
	stateRoot := t.TempDir()
	child := filepath.Join(stateRoot, "site-a")
	if err := os.Mkdir(child, 0o755); err != nil {
		t.Fatal(err)
	}
	want, err := filepath.EvalSymlinks(child)
	if err != nil {
		t.Fatal(err)
	}
	got, perr := ValidateRoot(child, stateRoot)
	if perr != nil {
		t.Fatalf("合法 root 被拒绝: %v", perr)
	}
	if got != want {
		t.Fatalf("返回 %q，期望真实路径 %q", got, want)
	}
	if _, perr := ValidateRoot(stateRoot, stateRoot); perr != nil {
		t.Fatalf("状态目录本身应合法: %v", perr)
	}
}

// TestValidateRootRejects 词法边界：空/相对/穿越/越权/NUL/反斜杠/不存在/非目录全部拒绝。
func TestValidateRootRejects(t *testing.T) {
	stateRoot := t.TempDir()
	filePath := filepath.Join(stateRoot, "plain.txt")
	if err := os.WriteFile(filePath, []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	cases := []string{
		"",
		"   ",
		"relative/path",
		"/etc",
		"/var/www/../etc",
		"/home/../etc",
		"/var/wwwsite",
		`/var/www\site`,
		"/var/www/\x00site",
		filepath.Join(stateRoot, "missing"),
		filePath,
	}
	for _, raw := range cases {
		if _, perr := ValidateRoot(raw, stateRoot); perr == nil || perr.Code != pluginapi.E_INVALID {
			t.Fatalf("ValidateRoot(%q) 未按 E_INVALID 拒绝: %v", raw, perr)
		}
	}
}

// TestValidateRootSymlinkEscape 状态目录内的软链指向目录外时必须拒绝（真实层二次校验）。
func TestValidateRootSymlinkEscape(t *testing.T) {
	base := t.TempDir()
	stateRoot := filepath.Join(base, "state")
	outside := filepath.Join(base, "outside")
	if err := os.MkdirAll(stateRoot, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(outside, 0o755); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(stateRoot, "escape")
	if err := os.Symlink(outside, link); err != nil {
		t.Skipf("当前文件系统不支持符号链接: %v", err)
	}
	if _, perr := ValidateRoot(link, stateRoot); perr == nil || perr.Code != pluginapi.E_INVALID {
		t.Fatalf("软链接逃逸未按 E_INVALID 拒绝: %v", perr)
	}
}

// TestAllowedRoots 白名单固定包含 /var/www、/opt、/home，并追加状态目录。
func TestAllowedRoots(t *testing.T) {
	roots := allowedRoots("/var/lib/kore/runtime-python")
	allowed := []string{"/var/www/site", "/opt/python3.12", "/home/pt/app", "/var/lib/kore/runtime-python/envs"}
	for _, path := range allowed {
		if !withinAny(roots, path) {
			t.Fatalf("白名单未覆盖 %q（roots=%v）", path, roots)
		}
	}
	rejected := []string{"/etc", "/var/wwwsite", "/homepage", "/opt2/python", "/var/lib/kore/runtime-python2"}
	for _, path := range rejected {
		if withinAny(roots, path) {
			t.Fatalf("白名单误包含 %q（roots=%v）", path, roots)
		}
	}
}
