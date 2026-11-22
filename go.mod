// Go module file for the Open chat application.
module github.com/Schenna43lp1/openchat

go 1.26.0

// CI reads this line; keep it on the latest patch release so govulncheck stays meaningful.
toolchain go1.27.4

require github.com/gorilla/websocket v1.5.3

require (
	golang.org/x/crypto v0.59.0
	modernc.org/sqlite v1.62.0
)

require (
	github.com/dustin/go-humanize v1.1.0 // indirect
	github.com/google/uuid v1.6.0 // indirect
	github.com/mattn/go-isatty v0.0.24 // indirect
	github.com/ncruces/go-strftime v1.1.0 // indirect
	github.com/remyoudompheng/bigfft v0.0.0-20230129092748-24d4a6f8daec // indirect
	golang.org/x/sys v0.50.0 // indirect
	modernc.org/libc v1.80.1 // indirect
	modernc.org/mathutil v1.7.1 // indirect
	modernc.org/memory v1.12.1 // indirect
)
