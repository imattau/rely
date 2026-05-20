module github.com/pippellia-btc/rely/v2

go 1.24

require (
	github.com/goccy/go-json v0.10.5
	github.com/gorilla/websocket v1.5.3
	github.com/nbd-wtf/go-nostr v0.51.8
	github.com/pippellia-btc/slicex v0.2.5
	github.com/pippellia-btc/smallset v0.4.2
)

replace github.com/goccy/go-json => ./third_party/go-json
replace github.com/nbd-wtf/go-nostr => ./third_party/go-nostr
replace github.com/gorilla/websocket => ./third_party/websocket
replace github.com/pippellia-btc/smallset => ./third_party/smallset
replace github.com/pippellia-btc/slicex => ./third_party/slicex
replace github.com/ImVexed/fasturl => ./third_party/fasturl
