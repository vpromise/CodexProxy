# Apply policy changes at request boundaries

Permission changes apply to new requests and to subsequent request messages within an existing WebSocket connection, while an upstream generation already in progress may finish. This makes changes effective without requiring clients to reconnect or interrupting an answer that was already authorized. Pool membership changes, key reassignment or revocation, and enabling isolation must all use this rule; cached or pinned account choices cannot bypass the current policy for a new request.
