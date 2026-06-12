// Package dns 是 fake-IP DNS 处理器:对 A 查询不真解析,返回保留段 fake IP
// 并记录 fakeIP↔域名;连接回到 TUN 时由 Dialer 反查域名做精确分流(防污染)。
package dns

import (
	"context"
	"fmt"
	"net/netip"
	"strings"

	"github.com/getbx/bx/internal/fakeip"
	"github.com/getbx/bx/internal/splitdns"
	"golang.org/x/net/dns/dnsmessage"
)

// Server 处理 DNS 查询,A 记录用 fake IP 应答。
type Server struct {
	pool   *fakeip.Pool
	ttl    uint32
	splits []SplitRoute
	fwd    Forwarder
	direct *splitdns.Set
}

func NewServer(pool *fakeip.Pool, ttl uint32) *Server {
	return &Server{pool: pool, ttl: ttl}
}

// SetSplit 配置 split 路由(匹配域名转发到内网 DNS 并把真实 IP 注册进 direct 集)。
func (s *Server) SetSplit(splits []SplitRoute, fwd Forwarder, direct *splitdns.Set) {
	s.splits = splits
	s.fwd = fwd
	s.direct = direct
}

// matchSplit 返回命中的 split 路由(无则 nil)。
func (s *Server) matchSplit(domain string) *SplitRoute {
	for i := range s.splits {
		if s.splits[i].Match.Match(domain) {
			return &s.splits[i]
		}
	}
	return nil
}

// Respond 解析一条 DNS 查询并构造应答:
//   - split 命中且 A 查询:转发内网 DNS,注册真实 IP,原样返回;
//   - split 命中且 AAAA:NODATA(逼客户端走 v4);
//   - 非 split A 查询:分配 fake IP 返回;
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

	domain := strings.ToLower(strings.TrimSuffix(q.Name.String(), "."))

	// split 命中且非 AAAA:转发到内网 DNS,注册真实 IP,原样返回。
	if rt := s.matchSplit(domain); rt != nil && q.Type != dnsmessage.TypeAAAA {
		resp, err := s.fwd.Forward(context.Background(), rt.Server, query)
		if err != nil {
			return s.servfail(query)
		}
		s.registerA(resp)
		return resp, nil
	}
	// split 命中 AAAA → 落到下面默认路径,因 q.Type!=TypeA 而成 NODATA(逼 v4)。

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

// registerA 把应答里所有 A 记录注册进 direct 集(强制直连)。
func (s *Server) registerA(msg []byte) {
	if s.direct == nil {
		return
	}
	var p dnsmessage.Parser
	if _, err := p.Start(msg); err != nil {
		return
	}
	if err := p.SkipAllQuestions(); err != nil {
		return
	}
	for {
		h, err := p.AnswerHeader()
		if err != nil {
			return
		}
		if h.Type == dnsmessage.TypeA {
			a, err := p.AResource()
			if err != nil {
				return
			}
			s.direct.Add(netip.AddrFrom4(a.A))
			continue
		}
		if err := p.SkipAnswer(); err != nil {
			return
		}
	}
}

// servfail 构造一个 RCode=SERVFAIL 的应答(转发失败时返回)。
func (s *Server) servfail(query []byte) ([]byte, error) {
	var p dnsmessage.Parser
	h, err := p.Start(query)
	if err != nil {
		return nil, err
	}
	q, err := p.Question()
	if err != nil {
		return nil, err
	}
	b := dnsmessage.NewBuilder(nil, dnsmessage.Header{
		ID:                 h.ID,
		Response:           true,
		OpCode:             h.OpCode,
		RecursionDesired:   h.RecursionDesired,
		RecursionAvailable: true,
		RCode:              dnsmessage.RCodeServerFailure,
	})
	if err := b.StartQuestions(); err != nil {
		return nil, err
	}
	if err := b.Question(q); err != nil {
		return nil, err
	}
	return b.Finish()
}
