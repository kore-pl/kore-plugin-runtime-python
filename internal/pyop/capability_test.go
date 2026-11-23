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
	verbs := []string{"versions", "info", "projects", "doctor", "paths"}
	if len(manifest.MethodCapabilities) != len(verbs) {
		t.Fatalf("method_capabilities 条数=%d，期望 %d", len(manifest.MethodCapabilities), len(verbs))
	}
	pattern := regexp.MustCompile(methodActionPattern)
	for _, verb := range verbs {
		key := "runtime-python." + verb
		capability, ok := manifest.MethodCapabilities[key]
		if !ok {
			t.Fatalf("缺少方法级能力声明 %q", key)
		}
		if capability != "system:read" {
			t.Fatalf("%s 能力=%q，期望 system:read（全部只读）", key, capability)
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
}
