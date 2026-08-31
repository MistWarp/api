module mistwarp.local/api

go 1.25.0

require (
	github.com/epiclabs-io/diff3 v0.0.0-20260520111523-3b1669897fb1 // indirect
	github.com/gorilla/websocket v1.5.3 // indirect
	github.com/mattn/go-sqlite3 v1.14.50 // indirect
	golang.org/x/crypto v0.55.0 // indirect
	mistwarp.local/gitinspection v0.0.0-00010101000000-000000000000 // indirect
	mistwarp.local/r2inventory v0.0.0-00010101000000-000000000000 // indirect
	mistwarp.local/runtimeinfo v0.0.0-00010101000000-000000000000 // indirect
)

replace mistwarp.local/gitinspection => ./native/gitinspection

replace mistwarp.local/r2inventory => ./native/r2inventory

replace mistwarp.local/runtimeinfo => ./native/runtimeinfo
