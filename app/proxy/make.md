编译 Linux AMD64（x86_64）
GOOS=linux GOARCH=amd64 go build -o vscode-proxy-linux-amd64

编译 Linux ARM64
GOOS=linux GOARCH=arm64 go build -o vscode-proxy-linux-arm64


command: --socket /tmp/vscode-server.sock --auth none --cert false /home/coder/project
network_mode: bridge