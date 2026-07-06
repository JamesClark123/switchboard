module github.com/jamesclark123/switchboard/services/switchboardd

go 1.26

require (
	github.com/google/uuid v1.6.0
	github.com/jamesclark123/switchboard/libs/switchboard-proto v0.0.0
	go.etcd.io/bbolt v1.3.11
	google.golang.org/grpc v1.81.1
	google.golang.org/protobuf v1.36.11
)

require (
	aead.dev/minisign v0.2.0 // indirect
	github.com/minio/selfupdate v0.6.0 // indirect
	golang.org/x/crypto v0.48.0 // indirect
)

require (
	github.com/creack/pty v1.1.24
	github.com/jamesclark123/switchboard/libs/switchboard-update v0.0.0
	golang.org/x/net v0.51.0 // indirect
	golang.org/x/sys v0.42.0 // indirect
	golang.org/x/text v0.34.0 // indirect
	google.golang.org/genproto/googleapis/rpc v0.0.0-20260226221140-a57be14db171 // indirect
)

replace github.com/jamesclark123/switchboard/libs/switchboard-proto => ../../libs/switchboard-proto

replace github.com/jamesclark123/switchboard/libs/switchboard-update => ../../libs/switchboard-update
