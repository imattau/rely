module github.com/pippellia-btc/rely/v2

go 1.25.0

require (
	github.com/goccy/go-json v0.10.5
	github.com/gorilla/websocket v1.5.3
	github.com/nbd-wtf/go-nostr v0.51.8
	github.com/pippellia-btc/slicex v0.2.5
	github.com/pippellia-btc/smallset v0.4.2
)

require (
	github.com/dustin/go-humanize v1.0.1 // indirect
	github.com/google/uuid v1.6.0 // indirect
	github.com/mattn/go-isatty v0.0.20 // indirect
	github.com/ncruces/go-strftime v1.0.0 // indirect
	github.com/remyoudompheng/bigfft v0.0.0-20230129092748-24d4a6f8daec // indirect
	golang.org/x/sys v0.42.0 // indirect
	modernc.org/libc v1.72.3 // indirect
	modernc.org/mathutil v1.7.1 // indirect
	modernc.org/memory v1.11.0 // indirect
	modernc.org/sqlite v1.50.1 // indirect
)

replace github.com/goccy/go-json => ./third_party/go-json

replace github.com/nbd-wtf/go-nostr => ./third_party/go-nostr

replace github.com/gorilla/websocket => ./third_party/websocket

replace github.com/pippellia-btc/smallset => ./third_party/smallset

replace github.com/pippellia-btc/slicex => ./third_party/slicex

replace github.com/ImVexed/fasturl => ./third_party/fasturl
