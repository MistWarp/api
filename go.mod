module mistwarp.local/api

go 1.26.0

require (
	github.com/aws/aws-sdk-go-v2 v1.45.1 // indirect
	github.com/aws/aws-sdk-go-v2/aws/protocol/eventstream v1.7.20 // indirect
	github.com/aws/aws-sdk-go-v2/credentials v1.20.1 // indirect
	github.com/aws/aws-sdk-go-v2/internal/configsources v1.5.1 // indirect
	github.com/aws/aws-sdk-go-v2/internal/endpoints/v2 v2.8.1 // indirect
	github.com/aws/aws-sdk-go-v2/internal/v4a v1.5.1 // indirect
	github.com/aws/aws-sdk-go-v2/service/internal/accept-encoding v1.13.19 // indirect
	github.com/aws/aws-sdk-go-v2/service/internal/checksum v1.11.1 // indirect
	github.com/aws/aws-sdk-go-v2/service/internal/presigned-url v1.14.1 // indirect
	github.com/aws/aws-sdk-go-v2/service/internal/s3shared v1.20.1 // indirect
	github.com/aws/aws-sdk-go-v2/service/s3 v1.109.1 // indirect
	github.com/aws/smithy-go v1.28.1 // indirect
	github.com/dustin/go-humanize v1.1.0 // indirect
	github.com/epiclabs-io/diff3 v0.0.0-20260520111523-3b1669897fb1 // indirect
	github.com/google/uuid v1.6.0 // indirect
	github.com/gorilla/websocket v1.5.3 // indirect
	github.com/mattn/go-isatty v0.0.24 // indirect
	github.com/mattn/go-sqlite3 v1.14.52 // indirect
	github.com/ncruces/go-strftime v1.1.0 // indirect
	github.com/remyoudompheng/bigfft v0.0.0-20230129092748-24d4a6f8daec // indirect
	golang.org/x/crypto v0.57.0 // indirect
	golang.org/x/image v0.47.0 // indirect
	golang.org/x/sys v0.49.0 // indirect
	golang.org/x/text v0.43.0 // indirect
	mistwarp.local/gitinspection v0.0.0-00010101000000-000000000000 // indirect
	mistwarp.local/r2inventory v0.0.0-00010101000000-000000000000 // indirect
	mistwarp.local/runtimeinfo v0.0.0-00010101000000-000000000000 // indirect
	mistwarp.local/sharecard v0.0.0-00010101000000-000000000000 // indirect
	mistwarp.local/storagefs v0.0.0-00010101000000-000000000000 // indirect
	modernc.org/libc v1.79.0 // indirect
	modernc.org/mathutil v1.7.1 // indirect
	modernc.org/memory v1.12.1 // indirect
	modernc.org/sqlite v1.61.1 // indirect
)

replace mistwarp.local/gitinspection => ./native/gitinspection

replace mistwarp.local/r2inventory => ./native/r2inventory

replace mistwarp.local/runtimeinfo => ./native/runtimeinfo

replace mistwarp.local/sharecard => ./native/sharecard

replace mistwarp.local/storagefs => ./native/storagefs
