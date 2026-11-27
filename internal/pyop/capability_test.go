package pyop

import (
	"encoding/json"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

// infoManifest 只声明本测试关心的字段；其余字段由内核校验。
type infoManifest struct {
	ID                 string            `json:"id"`
	Version            string            `json:"version"`
	Capabilities       []string          `json:"capabilities"`
	MethodCapabilities map[string]string `json:"method_capabilities"`
}

// methodActionPattern 与内核 registry 的方法级能力键格式一致。
const methodActionPattern = `^[a-z][a-zA-Z0-9]*(\.[a-z][a-zA-Z0-9]*)*$`

// privilegedCapabilities 是内核唯一认识的 6 项特权能力。
var privilegedCapabilities = map[string]bool{
	"system:read": true, "system:admin": true,
	"net:read": true, "net:write": true,
	"proc:read": true, "proc:write": true,
}

// expectedCapabilities 是本插件方法面的唯一期望表：动词 → 方法级能力。
var expectedCapabilities = map[string]string{
	"versions":      "system:read",
	"info":          "system:read",
	"projects":      "system:read",
	"doctor":        "system:read",
	"paths":         "system:read",
	"use":           "system:admin",
	"guard.add":     "system:admin",
	"guard.remove":  "system:admin",
	"guard.list":    "system:read",
	"guard.logs":    "system:read",
	"guard.start":   "proc:write",
	"guard.stop":    "proc:write",
	"guard.restart": "proc:write",
	"venv.create":   "system:admin",
	"venv.list":     "system:read",
	"pip.install":   "system:admin",
	"pip.list":      "system:read",
	"ext.install":   "system:admin",
	"ext.remove":    "system:admin",
	"ext.list":      "system:read",
}

// TestMethodCapabilitiesMatchDispatch 双向校验 info.json 的方法级能力声明与分发表：
// 声明键必须都能被 businessVerb 接受、都能解析到期望能力；分发表里的动词也必须有声明。
func TestMethodCapabilitiesMatchDispatch(t *testing.T) {
	payload, err := os.ReadFile(filepath.Join("..", "..", "info.json"))
	if err != nil {
		t.Fatalf("读取 info.json 失败: %v", err)
	}
	var manifest infoManifest
	if err := json.Unmarshal(payload, &manifest); err != nil {
		t.Fatalf("解析 info.json 失败: %v", err)
	}
	if manifest.ID != "runtime-python" {
		t.Fatalf("info.json.id=%q，期望 runtime-python", manifest.ID)
	}
	if len(manifest.MethodCapabilities) != len(expectedCapabilities) {
		t.Fatalf("method_capabilities 条数=%d，期望 %d", len(manifest.MethodCapabilities), len(expectedCapabilities))
	}
	pattern := regexp.MustCompile(methodActionPattern)
	for verb, want := range expectedCapabilities {
		key := "runtime-python." + verb
		capability, ok := manifest.MethodCapabilities[key]
		if !ok {
			t.Fatalf("缺少方法级能力声明 %q", key)
		}
		if capability != want {
			t.Fatalf("%s 能力=%q，期望 %q", key, capability, want)
		}
		if !privilegedCapabilities[capability] {
			t.Fatalf("%s 的能力 %q 不是 6 项特权能力之一", key, capability)
		}
		action, trimmed := strings.CutPrefix(key, "runtime-python.")
		if !trimmed || !pattern.MatchString(action) {
			t.Fatalf("能力键 %q 的动作段不符合内核格式", key)
		}
		normalized, accepted := businessVerb(key)
		if !accepted || normalized != verb {
			t.Fatalf("businessVerb(%q)=(%q,%v)，与 info.json 声明不一致", key, normalized, accepted)
		}
	}
	if _, accepted := businessVerb("runtime-python.exec"); accepted {
		t.Fatal("分发表不应接受未声明的方法 runtime-python.exec")
	}
	if len(manifest.Capabilities) == 0 {
		t.Fatal("插件级 capabilities 不能为空")
	}
	// 方法级只能收窄、不能跨域放大：每个宿主域都必须在插件级声明过。
	pluginDomains := map[string]bool{}
	for _, capability := range manifest.Capabilities {
		if index := strings.IndexByte(capability, ':'); index > 0 {
			pluginDomains[capability[:index]] = true
		}
	}
	for _, capability := range manifest.MethodCapabilities {
		index := strings.IndexByte(capability, ':')
		if index <= 0 {
			continue
		}
		if !pluginDomains[capability[:index]] {
			t.Fatalf("方法级能力 %q 的域未在插件级 capabilities 声明", capability)
		}
	}
}
