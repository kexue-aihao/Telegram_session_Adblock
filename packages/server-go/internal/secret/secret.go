// Package secret 集中所有与密钥材料相关的原语：静态加密、密码哈希、随机串。
//
// 全部用标准库实现，不引入第三方加密库 —— 加密代码是最不该「多一个依赖」
// 的地方，而 AES-GCM 与 Argon2id 在 Go 标准库（含 x/crypto）里都是完整可用的。
package secret

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base64"
	"errors"
	"fmt"
	"strings"

	"golang.org/x/crypto/argon2"
)

// ────────────────────────── 对称加密 ──────────────────────────

// Sealed 是一段 AES-256-GCM 密文的三要素。
//
// 为什么用 GCM 而不是 CBC：GCM 自带认证标签，能同时保证机密性与完整性 ——
// 有人改了库里的密文，解密会直接失败，而不是解出一段垃圾去骚扰 Telegram。
type Sealed struct {
	Cipher string // base64
	IV     string // base64，96 bit（GCM 标准长度）
	Tag    string // base64
}

// Seal 加密一段明文。每次调用都用全新的随机 IV。
func Seal(key []byte, plaintext string) (Sealed, error) {
	block, err := aes.NewCipher(key)
	if err != nil {
		return Sealed{}, fmt.Errorf("初始化 AES: %w", err)
	}
	gcm, err := cipher.NewGCM(block)
	if err != nil {
		return Sealed{}, fmt.Errorf("初始化 GCM: %w", err)
	}

	iv := make([]byte, gcm.NonceSize())
	if _, err := rand.Read(iv); err != nil {
		return Sealed{}, fmt.Errorf("生成随机 IV: %w", err)
	}

	// Seal 会把认证标签追加在密文末尾，这里拆开存储以便与既有表结构对齐
	out := gcm.Seal(nil, iv, []byte(plaintext), nil)
	tagStart := len(out) - gcm.Overhead()

	return Sealed{
		Cipher: base64.StdEncoding.EncodeToString(out[:tagStart]),
		IV:     base64.StdEncoding.EncodeToString(iv),
		Tag:    base64.StdEncoding.EncodeToString(out[tagStart:]),
	}, nil
}

// Open 解密。失败时的错误信息刻意写成可执行的指引。
func Open(key []byte, s Sealed) (string, error) {
	block, err := aes.NewCipher(key)
	if err != nil {
		return "", fmt.Errorf("初始化 AES: %w", err)
	}
	gcm, err := cipher.NewGCM(block)
	if err != nil {
		return "", fmt.Errorf("初始化 GCM: %w", err)
	}

	iv, err := base64.StdEncoding.DecodeString(s.IV)
	if err != nil {
		return "", fmt.Errorf("IV 不是合法 base64: %w", err)
	}
	body, err := base64.StdEncoding.DecodeString(s.Cipher)
	if err != nil {
		return "", fmt.Errorf("密文不是合法 base64: %w", err)
	}
	tag, err := base64.StdEncoding.DecodeString(s.Tag)
	if err != nil {
		return "", fmt.Errorf("认证标签不是合法 base64: %w", err)
	}

	plaintext, err := gcm.Open(nil, iv, append(body, tag...), nil)
	if err != nil {
		// 认证失败只有两种可能：密文被篡改，或 MASTER_KEY 换了。
		// 后者是运维常见操作，必须给出可执行的指引而不是一句 "cipher: message authentication failed"。
		return "", errors.New(
			"无法解密 bot token —— MASTER_KEY 与写入时不一致，或密文已损坏。" +
				"若确实更换过 MASTER_KEY，请在面板中重新录入该机器人的 token")
	}
	return string(plaintext), nil
}

// MaskToken 生成展示用掩码：`123456789:AAF…xY3`。
// 明文 token 在任何 API 响应里都不出现，管理员靠掩码辨认是哪一个 bot。
func MaskToken(plaintext string) string {
	colon := strings.IndexByte(plaintext, ':')
	if colon < 0 {
		if len(plaintext) <= 8 {
			return "••••"
		}
		return plaintext[:4] + "••••" + plaintext[len(plaintext)-2:]
	}
	id := plaintext[:colon]
	rest := plaintext[colon+1:]
	if len(rest) < 6 {
		return id + ":••••"
	}
	return id + ":" + rest[:3] + "••••••" + rest[len(rest)-3:]
}

// ────────────────────────── 随机与哈希 ──────────────────────────

// RandomToken 生成 URL-safe 随机串，用于会话 token。
func RandomToken(nBytes int) (string, error) {
	buf := make([]byte, nBytes)
	if _, err := rand.Read(buf); err != nil {
		return "", fmt.Errorf("生成随机串: %w", err)
	}
	return base64.RawURLEncoding.EncodeToString(buf), nil
}

// HashToken 对会话 token 做 SHA-256。
//
// 用 SHA-256 而不是 Argon2：token 本身就是 256 位的高熵随机值，
// 不存在被字典攻击的前提；这里要的只是「库被读走也无法直接冒用」。
func HashToken(token string) string {
	sum := sha256.Sum256([]byte(token))
	return fmt.Sprintf("%x", sum)
}

// ConstantTimeEqual 定长安全比较，避免用 `==` 比较密钥时泄露时序信息。
func ConstantTimeEqual(a, b string) bool {
	return subtle.ConstantTimeCompare([]byte(a), []byte(b)) == 1
}

// ────────────────────────── 密码哈希 ──────────────────────────

// Argon2id 参数取 OWASP 推荐值：19 MiB 内存、2 轮、单线程。
const (
	argonMemory  = 19 * 1024 // KiB
	argonTime    = 2
	argonThreads = 1
	argonKeyLen  = 32
	argonSaltLen = 16
)

// HashPassword 生成 PHC 格式的 Argon2id 串：
// $argon2id$v=19$m=19456,t=2,p=1$<salt>$<hash>
//
// 自己实现 PHC 编解码而不是引三方库：格式只有这一个，而多一个依赖
// 就多一个供应链面。参数写进串里，未来调参时旧密码依然可验证。
func HashPassword(password string) (string, error) {
	salt := make([]byte, argonSaltLen)
	if _, err := rand.Read(salt); err != nil {
		return "", fmt.Errorf("生成盐: %w", err)
	}

	hash := argon2.IDKey([]byte(password), salt, argonTime, argonMemory, argonThreads, argonKeyLen)

	return fmt.Sprintf("$argon2id$v=%d$m=%d,t=%d,p=%d$%s$%s",
		argon2.Version, argonMemory, argonTime, argonThreads,
		base64.RawStdEncoding.EncodeToString(salt),
		base64.RawStdEncoding.EncodeToString(hash),
	), nil
}

// VerifyPassword 校验密码。
//
// 刻意返回 error 而不是 bool：调用方需要区分「密码错」与「哈希串损坏」——
// 前者是常态路径，后者意味着库被改坏了，必须让人知道。
func VerifyPassword(encoded, password string) (bool, error) {
	parts := strings.Split(encoded, "$")
	if len(parts) != 6 || parts[0] != "" {
		return false, errors.New("密码哈希不是合法的 PHC 格式")
	}
	if parts[1] != "argon2id" {
		return false, fmt.Errorf("不支持的哈希算法：%s", parts[1])
	}

	var version int
	if _, err := fmt.Sscanf(parts[2], "v=%d", &version); err != nil {
		return false, fmt.Errorf("解析哈希版本失败：%w", err)
	}
	if version != argon2.Version {
		return false, fmt.Errorf("哈希版本不匹配：库中为 %d，当前支持 %d", version, argon2.Version)
	}

	var memory uint32
	var timeCost uint32
	var threads uint8
	if _, err := fmt.Sscanf(parts[3], "m=%d,t=%d,p=%d", &memory, &timeCost, &threads); err != nil {
		return false, fmt.Errorf("解析哈希参数失败：%w", err)
	}

	salt, err := base64.RawStdEncoding.DecodeString(parts[4])
	if err != nil {
		return false, fmt.Errorf("盐不是合法 base64：%w", err)
	}
	want, err := base64.RawStdEncoding.DecodeString(parts[5])
	if err != nil {
		return false, fmt.Errorf("哈希不是合法 base64：%w", err)
	}

	got := argon2.IDKey([]byte(password), salt, timeCost, memory, threads, uint32(len(want)))
	return subtle.ConstantTimeCompare(got, want) == 1, nil
}

// PasswordPolicyError 描述密码不合规的原因；nil 表示通过。
func PasswordPolicyError(password string) error {
	if len(password) < 8 {
		return errors.New("密码至少 8 位")
	}
	if len(password) > 256 {
		return errors.New("密码过长")
	}
	var hasLetter, hasDigit bool
	for _, r := range password {
		switch {
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z':
			hasLetter = true
		case r >= '0' && r <= '9':
			hasDigit = true
		}
	}
	if !hasLetter || !hasDigit {
		return errors.New("密码需要同时包含字母和数字")
	}
	return nil
}
