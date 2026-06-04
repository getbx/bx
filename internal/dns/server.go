// Package dns 是 fake-IP DNS 处理器:对 A 查询不真解析,返回保留段 fake IP
// 并记录 fakeIP↔域名;连接回到 TUN 时由 Dialer 反查域名做精确分流(防污染)。
package dns

import (
	"fmt"
	"strings"

	"github.com/getbx/bx/internal/fakeip"
	"golang.org/x/net/dns/dnsmessage"
)

// Server 处理 DNS 查询,A 记录用 fake IP 应答。
type Server struct {
	pool *fakeip.Pool
	ttl  uint32
}

func NewServer(pool *fakeip.Pool, ttl uint32) *Server {
	return &Server{pool: pool, ttl: ttl}
}

// Respond 解析一条 DNS 查询并构造应答:
//   - A 查询:分配 fake IP 返回;
//   - AAAA / 其他类型:NODATA(只回问题,无答案),逼客户端走 IPv4 fake-IP。
func (s *Server) Respond(query []byte) ([]byte, error) {
	var p dnsmessage.Parser
	h, err := p.Start(query)
	if err != nil {
		return nil, fmt.Errorf("解析 DNS 查询: %w", err)
	}
	q, err := p.Question()
	if err != nil {
		return nil, fmt.Errorf("DNS 查询无 question: %w", err)
	}

	b := dnsmessage.NewBuilder(nil, dnsmessage.Header{
		ID:                 h.ID,
		Response:           true,
		OpCode:             h.OpCode,
		RecursionDesired:   h.RecursionDesired,
		RecursionAvailable: true,
		RCode:              dnsmessage.RCodeSuccess,
	})
	if err := b.StartQuestions(); err != nil {
		return nil, err
	}
	if err := b.Question(q); err != nil {
		return nil, err
	}

	if q.Type == dnsmessage.TypeA && q.Class == dnsmessage.ClassINET {
		domain := strings.ToLower(strings.TrimSuffix(q.Name.String(), "."))
		ip := s.pool.Alloc(domain)
		if err := b.StartAnswers(); err != nil {
			return nil, err
		}
		if err := b.AResource(dnsmessage.ResourceHeader{
			Name:  q.Name,
			Type:  dnsmessage.TypeA,
			Class: dnsmessage.ClassINET,
			TTL:   s.ttl,
		}, dnsmessage.AResource{A: ip.As4()}); err != nil {
			return nil, err
		}
	}

	return b.Finish()
}
