package main

import (
	"crypto/rand"
	"encoding/base64"
	"fmt"
	"os"

	"golang.org/x/crypto/argon2"
)

func main() {
	if len(os.Args) < 2 {
		fmt.Fprintln(os.Stderr, "usage: hash-password <password>")
		os.Exit(1)
	}

	hash, err := GenerateCodeServerPasswordHash(os.Args[1])
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}

	fmt.Print(hash)
}

// 输入明文密码，输出 code-server 可用的 hashed-password (PHC 格式)
func GenerateCodeServerPasswordHash(password string) (string, error) {

	// 推荐的 Argon2id 参数 (匹配 code-server/Node.js 默认强度)
	const (
		time    = uint32(3)
		memory  = uint32(64 * 1024) // 64MB = 65536 KiB
		threads = uint8(4)
		keyLen  = uint32(32)
		saltLen = 16
	)

	// 1. 生成高强度随机 Salt
	salt := make([]byte, saltLen)
	if _, err := rand.Read(salt); err != nil {
		return "", fmt.Errorf("failed to generate random salt: %w", err)
	}

	// 2. 生成 Argon2id Hash
	hash := argon2.IDKey(
		[]byte(password),
		salt,
		time,
		memory,
		threads,
		keyLen,
	)

	// 3. Base64 编码（标准字典，无 '=' 填充）
	saltEncoded := base64.RawStdEncoding.EncodeToString(salt)
	hashEncoded := base64.RawStdEncoding.EncodeToString(hash)

	// 4. 拼接为标准 PHC 格式 (动态使用 argon2.Version)
	result := fmt.Sprintf(
		"$argon2id$v=%d$m=%d,t=%d,p=%d$%s$%s",
		argon2.Version, // 替代硬编码的 19
		memory,
		time,
		threads,
		saltEncoded,
		hashEncoded,
	)

	return result, nil
}
