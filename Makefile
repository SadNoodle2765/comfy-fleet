worker-windows:
	mkdir -p bin
	GOOS=windows GOARCH=amd64 go build -o bin/worker.exe ./cmd/worker

control-plane:
	mkdir -p bin
	go build -o bin/control-plane ./cmd/control-plane

test:
	go test ./...

test-verbose:
	go test -v ./...

clean:
	rm -rf bin