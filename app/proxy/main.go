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
	"os/signal"
	"strings"
	"syscall"
	"time"
)

func main() {
	upstreamSock := flag.String("socket", "/config/.target/code-server.sock", "upstream code-server unix socket")
	proxySock := flag.String("proxy-socket", "/config/.target/app.sock", "this proxy's own unix socket")
	prefix := flag.String("prefix", "/app/coder-docker", "URL prefix to strip")
	flag.Parse()

	// 启动前清理残留旧 Socket
	cleanupSockets(*proxySock, *upstreamSock)
	// 利用 defer 确保程序最终退出或 panic 时都能安全清理这两个 socket
	defer os.RemoveAll(*proxySock)
	defer os.RemoveAll(*upstreamSock)

	// 创建监听系统级终止信号的 Context，实现生命周期统一管理
	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()

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
			pr.Out.Header = pr.In.Header.Clone()
			pr.Out.URL.Path = path
			pr.Out.URL.RawPath = ""
		},
		Transport: &http.Transport{
			DialContext: func(ctx context.Context, network, addr string) (net.Conn, error) {
				var d net.Dialer
				return d.DialContext(ctx, "unix", *upstreamSock)
			},
			MaxIdleConns:          500,
			MaxIdleConnsPerHost:   200,
			IdleConnTimeout:       90 * time.Second,
			ExpectContinueTimeout: 1 * time.Second,
		},
		ModifyResponse: func(r *http.Response) error {
			if loc := r.Header.Get("Location"); strings.HasPrefix(loc, "/") && !strings.HasPrefix(loc, *prefix) {
				r.Header.Set("Location", *prefix+loc)
			}
			return nil
		},
		ErrorHandler: func(w http.ResponseWriter, r *http.Request, err error) {
			if errors.Is(err, syscall.ECONNREFUSED) || errors.Is(err, os.ErrNotExist) {
				http.Error(w, "code-server is not running or the socket does not exist", http.StatusServiceUnavailable)
				return
			}
			http.Error(w, "Bad Gateway", http.StatusBadGateway)
		},
	}

	listener, err := net.Listen("unix", *proxySock)
	if err != nil {
		log.Fatalf("failed to listen on proxy socket %s: %v", *proxySock, err)
	}

	if err := os.Chmod(*proxySock, 0660); err != nil {
		log.Printf("warning: failed to set socket permissions: %v", err)
	}

	handler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Upgrade") == "websocket" || strings.Contains(strings.ToLower(r.Header.Get("Connection")), "upgrade") {
			origin := r.Header.Get("Origin")
			if u, err := url.Parse(origin); err == nil && u.Host != "" {
				r.Header.Set("X-Forwarded-Proto", u.Scheme)
				r.Header.Set("X-Forwarded-Host", u.Host)
			}
		}
		r.URL.Scheme = "http"
		r.URL.Host = "unix"
		backend.ServeHTTP(w, r)
	})

	server := &http.Server{
		Handler:           handler,
		ReadHeaderTimeout: 5 * time.Second,
	}

	go func() {
		log.Printf("coder proxy listening on %s", *proxySock)
		log.Printf("upstream: unix://%s  prefix=%s", *upstreamSock, *prefix)

		if err := server.Serve(listener); err != nil && !errors.Is(err, http.ErrServerClosed) {
			log.Fatalf("server error: %v", err)
		}
	}()

	// 阻塞等待 Context 的取消信号 (收到退出信号或子进程崩溃)
	<-ctx.Done()
	log.Println("Context cancelled, shutting down proxy and child processes...")

	// 优雅关闭 HTTP 服务，拒绝新请求并等待已有的请求结束
	shutdownCtx, shutdownCancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer shutdownCancel()
	if err := server.Shutdown(shutdownCtx); err != nil {
		log.Printf("proxy shutdown error: %v", err)
	}

	log.Println("Shutdown complete")
	// 程序运行至此，由于有 defer，两个 Socket 都会被安全删除
}

// 辅助方法：清理残留 Socket
func cleanupSockets(socks ...string) {
	for _, sock := range socks {
		if err := os.RemoveAll(sock); err != nil {
			log.Fatalf("failed to remove old socket %s: %v", sock, err)
		}
	}
}
