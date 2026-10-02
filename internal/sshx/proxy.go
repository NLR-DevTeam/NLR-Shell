package sshx

import (
	"bufio"
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"strconv"
	"strings"
	"time"

	"nlrshell/internal/store"
)

// ParseProxyAddr checks a proxy address and returns it as host:port. A
// scheme in front, as in http://127.0.0.1:7890, is accepted and dropped.
func ParseProxyAddr(s string) (string, error) {
	s = strings.TrimSpace(s)
	if i := strings.Index(s, "://"); i >= 0 {
		s = s[i+3:]
	}
	s = strings.TrimSuffix(s, "/")
	host, port, err := net.SplitHostPort(s)
	if err != nil || host == "" || strings.Contains(host, "@") {
		return "", errors.New("代理地址无效")
	}
	if n, err := strconv.Atoi(port); err != nil || n <= 0 || n > 65535 {
		return "", errors.New("代理端口无效")
	}
	return net.JoinHostPort(host, port), nil
}

// dialProxy connects to addr through the proxy of the given kind, the way
// ProxyCommand nc -X connect (HTTP) or nc -X 5 (SOCKS5) does. The proxy
// resolves the target host name.
func dialProxy(kind, proxyAddr, addr string, timeout time.Duration) (net.Conn, error) {
	paddr, err := ParseProxyAddr(proxyAddr)
	if err != nil {
		return nil, err
	}
	d := net.Dialer{Timeout: timeout, KeepAlive: 30 * time.Second}
	c, err := d.Dial("tcp", paddr)
	if err != nil {
		return nil, proxyDialErr(paddr, err)
	}
	c.SetDeadline(time.Now().Add(timeout))
	var conn net.Conn
	switch kind {
	case store.ProxyHTTP:
		conn, err = httpConnect(c, addr)
	case store.ProxySOCKS5:
		conn, err = socks5Connect(c, addr)
	default:
		err = fmt.Errorf("未知的代理类型 %q", kind)
	}
	if err != nil {
		c.Close()
		var ne net.Error
		if errors.As(err, &ne) && ne.Timeout() {
			return nil, fmt.Errorf("代理 %s 无响应", paddr)
		}
		if errors.Is(err, io.EOF) || errors.Is(err, io.ErrUnexpectedEOF) {
			return nil, fmt.Errorf("代理 %s 关闭了连接", paddr)
		}
		return nil, err
	}
	c.SetDeadline(time.Time{})
	return conn, nil
}

// proxyDialErr describes a failure to reach the proxy itself, so that it
// is not mistaken for the SSH server refusing the connection.
func proxyDialErr(paddr string, err error) error {
	var ne net.Error
	s := err.Error()
	switch {
	case errors.As(err, &ne) && ne.Timeout():
		return fmt.Errorf("代理 %s 无响应", paddr)
	case strings.Contains(s, "actively refused") || strings.Contains(s, "connection refused"):
		return fmt.Errorf("无法连接代理 %s：代理未运行", paddr)
	}
	return fmt.Errorf("无法连接代理 %s：%v", paddr, err)
}

// httpConnect asks an HTTP proxy for a tunnel to addr.
func httpConnect(c net.Conn, addr string) (net.Conn, error) {
	req := "CONNECT " + addr + " HTTP/1.1\r\nHost: " + addr + "\r\nUser-Agent: NLRShell\r\n\r\n"
	if _, err := io.WriteString(c, req); err != nil {
		return nil, err
	}
	br := bufio.NewReader(c)
	resp, err := http.ReadResponse(br, &http.Request{Method: http.MethodConnect})
	if err != nil {
		return nil, err
	}
	if resp.StatusCode != http.StatusOK {
		resp.Body.Close()
		return nil, fmt.Errorf("代理拒绝连接 %s：%s", addr, resp.Status)
	}
	// The server may already have sent its banner behind the response.
	if br.Buffered() > 0 {
		return &bufConn{Conn: c, r: br}, nil
	}
	return c, nil
}

// bufConn is a connection whose first bytes were read into r.
type bufConn struct {
	net.Conn
	r *bufio.Reader
}

func (c *bufConn) Read(p []byte) (int, error) {
	if c.r.Buffered() > 0 {
		return c.r.Read(p)
	}
	return c.Conn.Read(p)
}

// socks5Replies describes the failure codes of RFC 1928.
var socks5Replies = map[byte]string{
	1: "代理服务器故障",
	2: "代理规则不允许",
	3: "网络不可达",
	4: "主机不可达",
	5: "连接被拒绝",
	6: "连接超时",
	7: "不支持的命令",
	8: "不支持的地址类型",
}

// socks5Connect asks a SOCKS5 proxy, without authentication, to connect
// to addr.
func socks5Connect(c net.Conn, addr string) (net.Conn, error) {
	host, portStr, err := net.SplitHostPort(addr)
	if err != nil {
		return nil, err
	}
	port, _ := strconv.Atoi(portStr)
	if _, err := c.Write([]byte{5, 1, 0}); err != nil {
		return nil, err
	}
	var b [4]byte
	if _, err := io.ReadFull(c, b[:2]); err != nil {
		return nil, err
	}
	switch {
	case b[0] != 5:
		return nil, errors.New("代理不是 SOCKS5 代理")
	case b[1] != 0:
		return nil, errors.New("SOCKS5 代理要求认证，暂不支持")
	}

	req := []byte{5, 1, 0}
	if ip := net.ParseIP(host); ip == nil {
		if len(host) > 255 {
			return nil, errors.New("主机名过长")
		}
		req = append(req, 3, byte(len(host)))
		req = append(req, host...)
	} else if ip4 := ip.To4(); ip4 != nil {
		req = append(req, 1)
		req = append(req, ip4...)
	} else {
		req = append(req, 4)
		req = append(req, ip.To16()...)
	}
	req = binary.BigEndian.AppendUint16(req, uint16(port))
	if _, err := c.Write(req); err != nil {
		return nil, err
	}

	if _, err := io.ReadFull(c, b[:4]); err != nil {
		return nil, err
	}
	if b[1] != 0 {
		msg := socks5Replies[b[1]]
		if msg == "" {
			msg = fmt.Sprintf("错误 %d", b[1])
		}
		return nil, fmt.Errorf("代理无法连接 %s：%s", addr, msg)
	}
	// Skip the bound address.
	var n int
	switch b[3] {
	case 1:
		n = 4
	case 4:
		n = 16
	case 3:
		if _, err := io.ReadFull(c, b[:1]); err != nil {
			return nil, err
		}
		n = int(b[0])
	default:
		return nil, errors.New("SOCKS5 代理的应答无效")
	}
	if _, err := io.CopyN(io.Discard, c, int64(n+2)); err != nil {
		return nil, err
	}
	return c, nil
}
