// Package policysync 是规则同步的纯判据:从服务器链接派生存放位置与钥匙、加密与解密那一团
// 规则。设计:docs/superpowers/specs/2026-09-29-policy-sync-via-own-server-design.md。
//
// 同一个人的 Mac 与 iPhone 用同一条链接,于是各自派生出同一个 BlobID 与同一把钥匙;VPS 只存
// 它解不开的密文(钥匙要 uuid,服务端的私钥没用)。bx server share 给朋友的是另一个 uuid ⇒
// 另一个 BlobID、另一把钥匙。本包纯(purity_test.go),会被编进 iPhone App。
package policysync

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/hkdf"
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	"github.com/getbx/bx/internal/blink"
	"github.com/getbx/bx/internal/linkkind"
	"github.com/getbx/bx/internal/singboxout"
)

// StorePort 是 VPS 上 sync-store 只听回环的端口;服务端的加固路由对它开唯一的例外(srvgen)。
const StorePort = 51781

// StoreAddr 是 sync-store 的监听地址。**只听回环**:不对公网开任何新端口(reality 的全部价值是
// VPS 在外面看起来只是一台转发到 cloudflare 的机器);客户端经隧道够得着它。
var StoreAddr = fmt.Sprintf("127.0.0.1:%d", StorePort)

// Policy 是同步的内容:只有路由意图。**不含链接、bypass 网段、hosts** —— 手机用不上的不上传。
type Policy struct {
	Version   int64    `json:"version"`
	UpdatedAt string   `json:"updated_at"`
	Global    bool     `json:"global"`
	Direct    []string `json:"direct"`
	Proxy     []string `json:"proxy"`
}

// Keys 是从一条链接派生出的存放位置与钥匙。
type Keys struct {
	BlobID string
	key    []byte
}

// ErrWrongKey:打不开 —— 钥匙不对(规则来自另一条链接),或内容被改过。两者在 GCM 下不可分,
// 也不该分:对用户都是「这份不是你的,已忽略」。
var ErrWrongKey = errors.New("this blob was sealed with a different link, or it was altered")

const magic = "bxps1"

// Derive 从链接(裸 vless:// 或 bx:// 换壳)派生 Keys。钥匙材料是 uuid ‖ pbk ‖ sid —— 只有持有
// 这条链接的设备才有;不是 reality 链接的一律拒绝。
func Derive(link string) (Keys, error) {
	link = strings.TrimSpace(link)
	candidates := []string{link}
	if strings.HasPrefix(link, "bx://") || strings.HasPrefix(link, "blink://") {
		all, err := blink.DecodeAll(link)
		if err != nil {
			return Keys{}, err
		}
		candidates = all
	}
	for _, l := range candidates {
		if linkkind.Kind(l) != linkkind.KindReality {
			continue
		}
		v, err := singboxout.ParseVless(l)
		if err != nil {
			return Keys{}, err
		}
		secret := []byte(v.UUID + "\x00" + v.PublicKey + "\x00" + v.ShortID)
		k, err := hkdf.Key(sha256.New, secret, []byte("bx-policy-sync"), "v1", 32)
		if err != nil {
			return Keys{}, err
		}
		id := hmac.New(sha256.New, k)
		id.Write([]byte("blob-id"))
		enc := hmac.New(sha256.New, k)
		enc.Write([]byte("enc"))
		return Keys{BlobID: hex.EncodeToString(id.Sum(nil))[:32], key: enc.Sum(nil)}, nil
	}
	return Keys{}, errors.New("rules can only be synced through a reality server link (vless://, or a bx:// link that contains one)")
}

// Seal 加密一份 Policy:magic ‖ nonce ‖ AES-256-GCM(json),附加数据是 BlobID(同一团密文挪到
// 别的位置就打不开)。
func Seal(k Keys, p Policy) ([]byte, error) {
	if len(k.key) == 0 {
		return nil, errors.New("no key")
	}
	plain, err := json.Marshal(p)
	if err != nil {
		return nil, err
	}
	aead, err := newAEAD(k.key)
	if err != nil {
		return nil, err
	}
	nonce := make([]byte, aead.NonceSize())
	if _, err := rand.Read(nonce); err != nil {
		return nil, err
	}
	out := append([]byte(magic), nonce...)
	return aead.Seal(out, nonce, plain, []byte(k.BlobID)), nil
}

// Open 解开 Seal 的输出。
func Open(k Keys, blob []byte) (Policy, error) {
	if len(k.key) == 0 {
		return Policy{}, errors.New("no key")
	}
	aead, err := newAEAD(k.key)
	if err != nil {
		return Policy{}, err
	}
	head := len(magic) + aead.NonceSize()
	if len(blob) < head+aead.Overhead() || string(blob[:len(magic)]) != magic {
		return Policy{}, errors.New("not a bx policy blob")
	}
	plain, err := aead.Open(nil, blob[len(magic):head], blob[head:], []byte(k.BlobID))
	if err != nil {
		return Policy{}, ErrWrongKey
	}
	var p Policy
	if err := json.Unmarshal(plain, &p); err != nil {
		return Policy{}, err
	}
	return p, nil
}

func newAEAD(key []byte) (cipher.AEAD, error) {
	block, err := aes.NewCipher(key)
	if err != nil {
		return nil, err
	}
	return cipher.NewGCM(block)
}
