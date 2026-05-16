:: Linux X86_64
set GOOS=linux
set GOARCH=amd64
go build -o throttle-linux-amd64 .

:: Linux ARM64
set GOOS=linux
set GOARCH=arm64
go build -o throttle-linux-arm64 .
