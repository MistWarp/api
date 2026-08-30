module mistwarp.local/api

go 1.25.0

require mistwarp.local/gitinspection v0.0.0
require mistwarp.local/gitmanagement v0.0.0

require (
	github.com/gorilla/websocket v1.5.3 // indirect
	github.com/mattn/go-sqlite3 v1.14.50 // indirect
	golang.org/x/crypto v0.55.0 // indirect
)

replace mistwarp.local/gitinspection => ./native/gitinspection
replace mistwarp.local/gitmanagement => ./native/gitmanagement
