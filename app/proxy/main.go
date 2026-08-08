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
	"strings"
	"syscall"
	"time"
)

func main() {
	upstreamSock := flag.String("socket", "/home/coder/.target/code-server.sock", "upstream code-server unix socket")
	proxySock := flag.String("proxy-socket", "/home/coder/.target/app.sock", "this proxy's own unix socket")
	prefix := flag.String("prefix", "/app/coder-docker", "URL prefix to strip")
	flag.Parse()

	// 启动前清理残留旧 Socket
	cleanupSockets(*proxySock, *upstreamSock)
	// 利用 defer 确保程序最终退出或 panic 时都能安全清理这两个 socket
	defer os.RemoveAll(*proxySock)
	defer os.RemoveAll(*upstreamSock)

	// 2. 创建监听系统级终止信号的 Context，实现生命周期统一管理
	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()

	// 3. 异步启动 code-server 进程 (将 ctx 传入)
	cmd := startCodeSocket(ctx, *upstreamSock)

	// 4. 开启子进程监控协程：如果子进程意外退出，则调用 stop() 联动关闭主进程
	go func() {
		if err := cmd.Wait(); err != nil {
			log.Printf("code-server exited with error: %v", err)
		} else {
			log.Println("code-server exited cleanly")
		}
		stop() // 触发 ctx.Done()，联动主程序进入关机流程
	}()

	// 5. 配置反向代理
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

	// 6. 将 HTTP 服务放到后台运行
	go func() {
		log.Printf("coder proxy listening on %s", *proxySock)
		log.Printf("upstream: unix://%s  prefix=%s", *upstreamSock, *prefix)

		if err := server.Serve(listener); err != nil && !errors.Is(err, http.ErrServerClosed) {
			log.Fatalf("server error: %v", err)
		}
	}()

	// 7. 阻塞等待 Context 的取消信号 (收到退出信号或子进程崩溃)
	<-ctx.Done()
	log.Println("Context cancelled, shutting down proxy and child processes...")

	// 优雅关闭 HTTP 服务，拒绝新请求并等待已有的请求结束
	shutdownCtx, shutdownCancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer shutdownCancel()
	if err := server.Shutdown(shutdownCtx); err != nil {
		log.Printf("proxy shutdown error: %v", err)
	}

	// 保底清理：向 code-server 所在的进程组发送 SIGTERM（优雅终止），而不是 SIGKILL
	if cmd.Process != nil {
		_ = syscall.Kill(-cmd.Process.Pid, syscall.SIGTERM)
	}

	log.Println("Shutdown complete")
	// 程序运行至此，由于有 defer，两个 Socket 都会被安全删除
}

func startCodeSocket(ctx context.Context, socketPath string) *exec.Cmd {
	node := "/usr/lib/code-server/lib/node"
	entry := "/usr/lib/code-server/out/node/entry.js"

	args := []string{
		entry,
		"--socket", socketPath,
		"--user-data-dir", "/home/coder/.data",
		"--auth", "none",
		"--cert", "false",
		"/home/coder/project",
	}

	cmd := exec.CommandContext(ctx, node, args...)
	cmd.Stdin = os.Stdin
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr

	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	log.Printf("Starting code-server: %s %v", node, args)

	if err := cmd.Start(); err != nil {
		log.Fatalf("failed to start code-server: %v", err)
	}

	return cmd
}

// 辅助方法：清理残留 Socket
func cleanupSockets(socks ...string) {
	for _, sock := range socks {
		if err := os.RemoveAll(sock); err != nil {
			log.Fatalf("failed to remove old socket %s: %v", sock, err)
		}
	}
}
