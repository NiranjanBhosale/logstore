MODULE := github.com/NiranjanBhosale/logstore

.PHONY: proto
proto:
	mkdir -p internal/replication/replicationpb
	protoc \
	  --go_out=. --go_opt=module=$(MODULE) \
	  --go-grpc_out=. --go-grpc_opt=module=$(MODULE) \
	  proto/replication/v1/replication.proto