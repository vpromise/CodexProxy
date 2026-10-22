# Enforce isolation across local native routes

The first version of isolation covers locally managed native Codex and Claude accounts across ordinary HTTP, streaming, WebSocket, search, and Live/Realtime routes. Home remote dispatch and direct plugin execution are rejected while isolation is enabled because their execution paths cannot enforce the local account-pool grants. This limits the first version to paths with enforceable account selection rather than exposing an alternate route around the same key's permissions.
