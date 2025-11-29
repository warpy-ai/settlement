export GO_PATH=~/go
export PATH=$PATH:/$GO_PATH/bin
mkdir -p proto/gen && protoc --go_out=proto/gen --go_opt=paths=source_relative --go-grpc_out=proto/gen --go-grpc_opt=paths=source_relative proto/worker.proto