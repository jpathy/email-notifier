.PHONY: clean rpcgen

EXE ?= sesrcvr
PBFILES := ctrlrpc/ctrl_message.pb.go ctrlrpc/ctrl_message_grpc.pb.go

$(EXE): *.go ctrlrpc/*.go ../go.mod ../go.sum
	go build -o $@ -tags 'sqlite_json1 sqlite_foreign_keys' $(GOBUILDARGS)
$(PBFILES) &: ctrlrpc/ctrl_message.proto
	protoc --go_out=. --go_opt=paths=source_relative --go-grpc_out=. --go-grpc_opt=paths=source_relative ctrlrpc/ctrl_message.proto
rpcgen: $(PBFILES)
clean:
	rm -f $(EXE)
