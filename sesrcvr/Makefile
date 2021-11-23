.PHONY: clean

sesrcvr: *.go
	go build -tags 'sqlite_json1 sqlite_foreign_keys' $(GOBUILDARGS)
rpcgen:
	protoc --go_out=. --go_opt=paths=source_relative --go-grpc_out=. --go-grpc_opt=paths=source_relative ctrlrpc/ctrl_message.proto
clean:
	rm -f sesrcvr
