# 声明伪目标（不与同名文件冲突）
.PHONY: all build-hash build-proxy pack clean

# 定义通用环境变量
GO_ENV = CGO_ENABLED=0 GOOS=linux GOARCH=amd64
BIN_DIR = app/bin

# 默认执行的目标 (直接运行 make 就会执行这个)
all: build-hash build-proxy pack

build-hash:
	@echo "==> 正在编译 Installer (code-password-hash)..."
	@cd app/gocmd/installer && $(GO_ENV) go build -o ../../bin/code-password-hash .

build-proxy:
	@echo "==> 正在编译 Proxy (coder-proxy-linux-amd64)..."
	@cd app/gocmd/proxy && $(GO_ENV) go build -o ../../bin/coder-proxy-linux-amd64 .

pack:
	@rm -f coder-docker.fpk
	@echo "==> 正在打包 fpk..."
	@fnpack build
	@rm -f $(BIN_DIR)/code-password-hash
	@rm -f $(BIN_DIR)/coder-proxy-linux-amd64
	@cp ./coder-docker.fpk /vol1/1000/
	@echo "==> 打包完成！"

clean:
	@echo "==> 清理构建产物..."
	@rm -f $(BIN_DIR)/code-password-hash
	@rm -f $(BIN_DIR)/coder-proxy-linux-amd64
	@rm -f coder-docker.fpk