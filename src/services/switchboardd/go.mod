module github.com/jamesclark123/switchboard/services/switchboardd

go 1.26

require (
	github.com/google/uuid v1.6.0
	github.com/jamesclark123/switchboard/libs/switchboard-dial v0.0.0
	github.com/jamesclark123/switchboard/libs/switchboard-proto v0.0.0
	github.com/jamesclark123/switchboard/libs/switchboard-termview v0.0.0
	github.com/modelcontextprotocol/go-sdk v1.8.0
	go.etcd.io/bbolt v1.3.11
	google.golang.org/grpc v1.81.1
	google.golang.org/protobuf v1.36.11
)

require (
	aead.dev/minisign v0.2.0 // indirect
	github.com/charmbracelet/colorprofile v0.4.1 // indirect
	github.com/charmbracelet/x/ansi v0.11.6 // indirect
	github.com/charmbracelet/x/cellbuf v0.0.15 // indirect
	github.com/charmbracelet/x/term v0.2.2 // indirect
	github.com/clipperhouse/displaywidth v0.9.0 // indirect
	github.com/clipperhouse/stringish v0.1.1 // indirect
	github.com/clipperhouse/uax29/v2 v2.5.0 // indirect
	github.com/google/jsonschema-go v0.4.3 // indirect
	github.com/lucasb-eyer/go-colorful v1.3.0 // indirect
	github.com/mattn/go-runewidth v0.0.19 // indirect
	github.com/minio/selfupdate v0.6.0 // indirect
	github.com/rivo/uniseg v0.4.7 // indirect
	github.com/segmentio/asm v1.1.3 // indirect
	github.com/segmentio/encoding v0.5.4 // indirect
	github.com/xo/terminfo v0.0.0-20220910002029-abceb7e1c41e // indirect
	github.com/yosida95/uritemplate/v3 v3.0.2 // indirect
	golang.org/x/crypto v0.48.0 // indirect
	golang.org/x/oauth2 v0.36.0 // indirect
	golang.org/x/sync v0.20.0 // indirect
	golang.org/x/time v0.15.0 // indirect
)

require (
	github.com/creack/pty v1.1.24
	github.com/jamesclark123/switchboard/libs/switchboard-update v0.0.0
	golang.org/x/net v0.51.0 // indirect
	golang.org/x/sys v0.42.0 // indirect
	golang.org/x/text v0.34.0 // indirect
	google.golang.org/genproto/googleapis/rpc v0.0.0-20260226221140-a57be14db171
)

replace github.com/jamesclark123/switchboard/libs/switchboard-proto => ../../libs/switchboard-proto

replace github.com/jamesclark123/switchboard/libs/switchboard-update => ../../libs/switchboard-update

replace github.com/jamesclark123/switchboard/libs/switchboard-dial => ../../libs/switchboard-dial

replace github.com/jamesclark123/switchboard/libs/switchboard-termview => ../../libs/switchboard-termview
