# Examples

This directory contains practical examples demonstrating how to build custom Nostr relays using rely. Each example focuses on specific features and use cases.

## Getting Started

All examples can be run directly with:
```bash
cd <example-directory>
go run .
```

## Available Examples

### [basic](./basic)
The simplest possible relay implementation. Great starting point for understanding rely's core concepts.

### [auth](./auth)
Demonstrates NIP-42 authentication implementation.

### [logger](./logger)
Shows how to integrate custom logging with rely.

### [blacklist](./blacklist)
Implements IP-based blacklist triggered by "suspicious" activity.

### [ip-rate](./ip-rate)
Rate limiting based on IP addresses.

### [count](./count)
Implements NIP-45 event counting.

### [anti-crawlers](./anti-crawlers)
Protects relay from automated crawlers and scrapers.

### [wot](./wot)
Web of Trust implementation for relay access control.

### [sparing](./sparing)
Demonstrates resource-efficient relay operation.

### [nip11](./nip11)
Implements NIP-11 relay information document.

### [dvm](./dvm)
Data Vending Machine (DVM) relay example.

### [api](./api)
Shows how to embed rely in a custom HTTP server with additional API endpoints.

## Contributing

Have an example that showcases a useful pattern? Feel free to contribute by opening a pull request!
