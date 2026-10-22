# CodexProxy

CodexProxy connects client requests to upstream model accounts. These terms distinguish client access from upstream credentials.

## Language

**Client API key**:
A credential a client presents to access CodexProxy, identified for administration by a name or note. It is the unit of client access assignment.
_Avoid_: Upstream API key, account token

**Upstream account**:
A configured identity or credential through which CodexProxy accesses an upstream provider. Its membership identity continues through maintenance of that same account and ends when the account is deleted.
_Avoid_: User, client API key

**Upstream provider**:
The service that fulfills a model request, such as Codex or Claude, independently of the client's request format.
_Avoid_: API endpoint, request format

**Account pool**:
A named set of upstream accounts from one provider. Multiple client API keys can reference the same pool, and different pools can contain the same upstream account.
_Avoid_: Dedicated account, user

**Isolation mode**:
An access policy that restricts a client API key's requests to its assigned account pool.
_Avoid_: Dedicated quota, exclusive account ownership
