package main

import (
	"encoding/json"
	"fmt"
	"log"
	"os"
	"os/exec"
)

func initialize() {
	flagFile := "/home/coder/.target/.initialized"

	if _, err := os.Stat(flagFile); err == nil {
		return
	}

	dirs := []string{
		"/home/coder/.data",
		"/home/coder/.config",
		"/home/coder/project",
	}

	for _, dir := range dirs {
		os.MkdirAll(dir, 0750)
		os.Chown(dir, 1000, 1001)
		os.Chmod(dir, 0750)
	}

	// 执行所有者修正
	chownDirs("0:0", "/home/coder/.target")

	file, err := os.Create(flagFile)

	if err == nil {
		os.Chown(flagFile, 0, 0)
		os.Chmod(flagFile, 0644)
		file.Close()
	}
}

// 修改指定路径的所有者
func chownDirs(owner string, path string) {
	cmd := exec.Command("chown", "-R", owner, path)
	out, err := cmd.CombinedOutput()
	if err != nil {
		log.Printf("chown failed for %s: %v output=%s", path, err, out)
	}
}

// 辅助方法：更换源
func patchProductJSON() error {
	filePath := "/usr/lib/code-server/lib/vscode/product.json"

	// 2. 读取原始 JSON 文件
	data, err := os.ReadFile(filePath)
	if err != nil {
		return fmt.Errorf("读取文件失败 (%s): %w", filePath, err)
	}

	// 3. 将 JSON 解析为泛型 map
	var product map[string]interface{}
	if err := json.Unmarshal(data, &product); err != nil {
		return fmt.Errorf("解析 JSON 失败: %w", err)
	}

	// 4. 覆盖指定的字段
	product["linkProtectionTrustedDomains"] = []string{
		"https://open-vsx.org",
		"https://marketplace.visualstudio.com",
	}

	product["extensionsGallery"] = map[string]interface{}{
		"serviceUrl":         "https://marketplace.visualstudio.com/_apis/public/gallery",
		"cacheUrl":           "https://vscode.blob.core.windows.net/gallery/index",
		"itemUrl":            "https://marketplace.visualstudio.com/items",
		"controlUrl":         "",
		"recommendationsUrl": "",
	}

	// 5. 格式化并序列化回 JSON（使用两个空格缩进）
	out, err := json.MarshalIndent(product, "", "  ")
	if err != nil {
		return fmt.Errorf("序列化 JSON 失败: %w", err)
	}

	// 6. 原子性写入：在同目录下生成 .tmp 文件，防止跨磁盘重命名报错
	tmp := filePath + ".tmp"
	if err = os.WriteFile(tmp, out, 0644); err != nil {
		return fmt.Errorf("写入临时文件失败: %w", err)
	}

	// 7. 重命名覆盖原文件
	if err = os.Rename(tmp, filePath); err != nil {
		// 尽量做个清理操作，删除遗留的临时文件
		_ = os.Remove(tmp)
		return fmt.Errorf("替换原文件失败: %w", err)
	}

	return nil
}
