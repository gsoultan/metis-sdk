// The SDK is its own module and its own repository on purpose. Living inside
// the server's module would make every consumer inherit the engine's
// dependency graph — GORM, goja, RabbitMQ, OpenTelemetry — to call an HTTP
// API. A client library that costs that much to import does not get imported.
//
// The language version tracks the server's, so the two repositories build on
// one toolchain. Note that this directive is inherited by every consumer's
// build: a project on an older Go cannot import this module without upgrading.
// The code itself needs far less — 1.24, set by the omitzero struct tags and
// testing.T.Context — so the floor can be lowered whenever that matters more
// than staying in step with the engine.
module github.com/gsoultan/metis-sdk

go 1.27.0
