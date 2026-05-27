module github.com/iicpc/dbhp/control-plane-api

go 1.22

require (
	github.com/iicpc/dbhp/shared-go v0.0.0
	github.com/jackc/pgx/v5 v5.5.5
	github.com/prometheus/client_golang v1.19.0
	github.com/prometheus/client_model v0.6.0
	go.uber.org/zap v1.27.0
	google.golang.org/grpc v1.63.2
)

replace github.com/iicpc/dbhp/shared-go => ../../libs/shared-go

require (
	github.com/beorn7/perks v1.0.1 // indirect
	github.com/cespare/xxhash/v2 v2.2.0 // indirect
	github.com/jackc/pgpassfile v1.0.0 // indirect
	github.com/jackc/pgservicefile v0.0.0-20221227161230-091c0ba34f0a // indirect
	github.com/jackc/puddle/v2 v2.2.1 // indirect
	github.com/prometheus/common v0.48.0 // indirect
	github.com/prometheus/procfs v0.12.0 // indirect
	go.uber.org/multierr v1.10.0 // indirect
	golang.org/x/crypto v0.21.0 // indirect
	golang.org/x/net v0.22.0 // indirect
	golang.org/x/sync v0.6.0 // indirect
	golang.org/x/sys v0.18.0 // indirect
	golang.org/x/text v0.14.0 // indirect
	google.golang.org/genproto/googleapis/rpc v0.0.0-20240227224415-6ceb2ff114de // indirect
	google.golang.org/protobuf v1.34.1 // indirect
)
