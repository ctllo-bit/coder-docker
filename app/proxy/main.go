package main

import (
	"context"
	"errors"
	"flag"
	"log"
	"net"
	"net/http"
	"net/http/httputil"
	"net/url"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"strings"
	"syscall"
	"time"
)

var (
	rootDir          = "/usr/lib/code-server"
	codeServerSocket = flag.String("socket", "/home/coder/target/code-server.sock", "upstream code-server unix socket")
	proxySocket      = flag.String("proxy-socket", "/home/coder/target/app.sock", "this proxy's own unix socket")
	prefix           = flag.String("prefix", "/app/vs-code", "URL prefix to strip before forwarding")
)

func main() {
	flag.Parse()

	// 启动前清理残留旧 Socket
	if err := os.RemoveAll(*proxySocket); err != nil {
		log.Fatalf("failed to remove old proxy socket: %v", err)
	}
	if err := os.RemoveAll(*codeServerSocket); err != nil {
		log.Fatalf("failed to remove old proxy socket: %v", err)
	}

	// 2. 异步启动 code-server 进程
	cmd := startCodeSocket()

	backend := &httputil.ReverseProxy{
		Rewrite: func(pr *httputil.ProxyRequest) {
			path := pr.In.URL.Path
			if trimmed, found := strings.CutPrefix(path, *prefix); found {
				if trimmed == "" || trimmed[0] != '/' {
					path = "/" + trimmed
				} else {
					path = trimmed
				}
			}

			pr.Out.Header = pr.In.Header.Clone() // 拷贝修正后 Header传送给code-server
			pr.Out.URL.Path = path               //修改后的 path 要赋值给 Out.URL
			pr.Out.URL.RawPath = ""              //显式清空 RawPath，防止含有 %20, %2F 等特殊字符的 URL 仍然使用旧路径
		},
		Transport: &http.Transport{
			DialContext: func(ctx context.Context, network, addr string) (net.Conn, error) {
				var d net.Dialer
				return d.DialContext(ctx, "unix", *codeServerSocket)
			},
			MaxIdleConns:          500,
			MaxIdleConnsPerHost:   200,
			IdleConnTimeout:       90 * time.Second,
			ExpectContinueTimeout: 1 * time.Second,
		},
		ModifyResponse: func(r *http.Response) error {
			// 修改重定向 Header 加上 Prefix
			if loc := r.Header.Get("Location"); strings.HasPrefix(loc, "/") && !strings.HasPrefix(loc, *prefix) {
				r.Header.Set("Location", *prefix+loc)
			}
			return nil
		},
		ErrorHandler: func(w http.ResponseWriter, r *http.Request, err error) {
			log.Printf("proxy error: %s %s -> %v", r.Method, r.URL.Path, err)

			// 使用 errors.Is 精确匹配底层系统错误，替代脆弱的字符串匹配
			if errors.Is(err, syscall.ECONNREFUSED) || errors.Is(err, os.ErrNotExist) {
				http.Error(w, "code-server is not running or the socket does not exist", http.StatusServiceUnavailable)
				return
			}

			http.Error(w, "Bad Gateway", http.StatusBadGateway)
		},
	}

	listener, err := net.Listen("unix", *proxySocket)
	if err != nil {
		log.Fatalf("failed to listen on proxy socket %s: %v", *proxySocket, err)
	}
	// 利用 defer 确保退出或 panic 时都能安全清理 Socket
	defer os.RemoveAll(*proxySocket)

	if err := os.Chmod(*proxySocket, 0666); err != nil {
		log.Printf("warning: failed to set socket permissions: %v", err)
	}

	// 包装 handler：无尾斜杠时 301 重定向到带尾斜杠版本，避免在 /app/coder 下错误拼接路径为 /app/...
	handler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// WebSocket 请求，从 Origin 中解析并提取出 X-Forwarded-Proto 和 X-Forwarded-Host 是解决 WebSocket 跨域和 code-server 校验失败
		if r.Header.Get("Upgrade") == "websocket" || strings.Contains(strings.ToLower(r.Header.Get("Connection")), "upgrade") {
			origin := r.Header.Get("Origin")
			// 解析 Origin URL
			u, err := url.Parse(origin)
			if err != nil || u.Host == "" {
				return
			}
			r.Header.Set("X-Forwarded-Proto", u.Scheme)
			r.Header.Set("X-Forwarded-Host", u.Host)
		}
		r.URL.Scheme = "http" // 设置 Scheme
		r.URL.Host = "unix"   // 设置 Host
		backend.ServeHTTP(w, r)
	})

	server := &http.Server{
		Handler: handler,
		// 设置读取请求头的超时时间，防止 Slowloris 攻击。
		// 注意：千万不要设置 ReadTimeout/WriteTimeout，否则会切断 code-server 的 WebSocket。
		ReadHeaderTimeout: 5 * time.Second,
	}

	// 优雅停机逻辑
	go func() {
		sigCh := make(chan os.Signal, 1)
		signal.Notify(sigCh, syscall.SIGINT, syscall.SIGTERM)
		<-sigCh
		log.Println("shutting down proxy...")

		// 通知子进程 code-server 退出
		if cmd.Process != nil {
			cmd.Process.Signal(syscall.SIGTERM)
		}

		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		if err := server.Shutdown(ctx); err != nil {
			log.Printf("proxy shutdown error: %v", err)
		}
	}()

	log.Printf("coder proxy listening on %s", *proxySocket)
	log.Printf("upstream: unix://%s  prefix=%s", *codeServerSocket, *prefix)

	if err := server.Serve(listener); err != nil && !errors.Is(err, http.ErrServerClosed) {
		log.Fatalf("server error: %v", err)
	}
}

// 保持独立函数结构，返回 cmd 对象以便主进程管理其生命周期
func startCodeSocket() *exec.Cmd {
	node := filepath.Join(rootDir, "lib", "node")
	entry := filepath.Join(rootDir, "out", "node", "entry.js")

	// 注入内部 Socket 参数，让 code-server 知道该监听哪里
	//args := append([]string{entry}, codeServerArgs...)
	args := append([]string{entry}, "--socket", *codeServerSocket)

	cmd := exec.Command(node, args...)
	cmd.Stdin = os.Stdin
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr

	log.Printf("Starting code-server: %s %v", node, args)

	// 关键修复：使用 Start() 而不是 Run()，防止主程序被阻塞在这里
	if err := cmd.Start(); err != nil {
		log.Fatalf("failed to start code-server: %v", err)
	}

	// 后台等待进程退出并清理
	go func() {
		if err := cmd.Wait(); err != nil {
			log.Printf("code-server exited with error: %v", err)
		} else {
			log.Printf("code-server exited cleanly")
		}
		// 子进程死掉后，主程序清理文件并自我终结，触发 docker restart
		os.RemoveAll(*proxySocket)
		os.RemoveAll(*codeServerSocket)
		os.Exit(1)
	}()

	return cmd
}
