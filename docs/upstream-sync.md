# Upstream sync ledger

Updated: 2026-09-18. This is the checkpoint for selective backports from `router-for-me/CLIProxyAPI` into `vpromise/CodexProxy`.

**The reviewed upstream boundary is not a full merge boundary.** Only the behavior listed as published below has been integrated in the indicated scope. Original upstream commits may remain outside this fork's ancestry, so GitHub's behind count is not a count of missing fixes. Published source and server deployment are separate states.

## Current checkpoint

| Item | Recorded state |
| --- | --- |
| Last reviewed upstream main | [8c664b2f](https://github.com/router-for-me/CLIProxyAPI/commit/8c664b2fede5c83b919be1df9b01057ec4e4c950) (`8c664b2fede5c83b919be1df9b01057ec4e4c950`) |
| Release at that review | [v7.3.6](https://github.com/router-for-me/CLIProxyAPI/releases/tag/v7.3.6), published 2026-09-17 05:53:32 UTC |
| Latest verified published implementation | [ec916ab8](https://github.com/vpromise/CodexProxy/commit/ec916ab8cce092707956d14d7eaf1c679b3406b8) (`ec916ab8cce092707956d14d7eaf1c679b3406b8`) on `main` |
| Published source tree | `8a7dc4270ee69b84e26314ee3e6a4eb168250944` |
| Latest publication | Remaining three v7.3.3 fixes across 17 source/config/test paths, plus this ledger and README links |
| Unpublished selected fixes | None; all four selected v7.3.3 fixes and both v7.3.6 batches are published |
| Deployment | The September 17 and 18 publications were not deployed in these workflows; this ledger does not assert the current server version |
| Client fingerprints | Claude Code **2.1.258**; Codex **0.154.0** |
| Current selection policy | Keep changes minimal; no third batch from the remaining backlog is approved |

The published implementation commit above deliberately excludes the ledger and README changes, which are recorded in a separate documentation commit. The reviewed upstream boundary remains unchanged by this publication.

## Published on 2026-09-18

| Upstream source | Published behavior | Boundary | Fork commit |
| --- | --- | --- | --- |
| [2bcebaa8](https://github.com/router-for-me/CLIProxyAPI/commit/2bcebaa89c98871bded27cc8a8c1c4c97be25623) | Responses-to-Claude tool-history repair | Preserve explicit call IDs, repair interrupted tool turns and retain orphan outputs as ordinary user content | [ec916ab8](https://github.com/vpromise/CodexProxy/commit/ec916ab8cce092707956d14d7eaf1c679b3406b8) |
| [7bbfeaf8](https://github.com/router-for-me/CLIProxyAPI/commit/7bbfeaf8a7acf2cd5a834dcb0842539fe6aabc2b) | Codex reasoning-content normalization | Normalize Codex-specific reasoning content and empty summaries across relevant request paths; keep generic compact behavior separate | [ec916ab8](https://github.com/vpromise/CodexProxy/commit/ec916ab8cce092707956d14d7eaf1c679b3406b8) |
| [cb73cd99](https://github.com/router-for-me/CLIProxyAPI/commit/cb73cd9936116848274afad42a41a4079325d383) | Optional Codex bootstrap-event buffering | Closed allowlist; default off; bounded by 48 frames and 1 MiB | [ec916ab8](https://github.com/vpromise/CodexProxy/commit/ec916ab8cce092707956d14d7eaf1c679b3406b8) |

These three repairs were implemented locally on September 15 and remained uncommitted after the September 17 publication. Together with the already published `8c984672` prerequisite below, all four selected v7.3.3 repairs are now published. This completes existing work; the deferred backlog below is unchanged.

The implementation tree exactly reproduces the combined source tree verified on September 17: full tests passed in **67 packages**, targeted race checks passed in **7 packages**, and the server build passed. Those results were reused after exact Git tree comparison; formatting of the changed Go files and patch whitespace were checked again. No tests were rerun or Go test/build caches regenerated for this publication. The existing macOS exclusions remain `TestClaudeStandardRoundTripperBindsLocalIP` and `TestClaudeStandardRoundTripperHTTPSProxyKeepsBindAndProxyTLS` (127.0.0.2 binding limitation).

## Published on 2026-09-17

| Group | Upstream source | Published behavior | Fork commit |
| --- | --- | --- | --- |
| Required existing repair | [8c984672](https://github.com/router-for-me/CLIProxyAPI/commit/8c984672a66ab824f96aace243248ec2a67403d5) | Preserve unpaired function/custom-tool outputs as user content, including text and media; keep paired results as tool messages | [4b3ce8fb](https://github.com/vpromise/CodexProxy/commit/4b3ce8fb654811004513bd42c3e4748758147a63) |
| Batch 1 | [7c32971b](https://github.com/router-for-me/CLIProxyAPI/commit/7c32971b91c8bce3a199833d1e738bd883e6b92f) | Complete Claude streams at the terminal event | [7b497e56](https://github.com/vpromise/CodexProxy/commit/7b497e5679ac5a96b7366bd210d6d60e19113c33) |
| Batch 1 | [772c63c8](https://github.com/router-for-me/CLIProxyAPI/commit/772c63c8e2e26aa1d3998984c78e0a5c4e3b6739) | Preserve truncation when translating tool-call completion | [7b497e56](https://github.com/vpromise/CodexProxy/commit/7b497e5679ac5a96b7366bd210d6d60e19113c33) |
| Batch 1 | [923a8c30](https://github.com/router-for-me/CLIProxyAPI/commit/923a8c30dcb38f018aae9024d6e46714f1586d1c) | Recognize codex_exec clients | [7b497e56](https://github.com/vpromise/CodexProxy/commit/7b497e5679ac5a96b7366bd210d6d60e19113c33) |
| Batch 2 | [77820cb2](https://github.com/router-for-me/CLIProxyAPI/commit/77820cb2f46d909873b66ae381225e178c48a898) | Alternative reasoning fields in OpenAI-to-Claude responses | [c9488f2b](https://github.com/vpromise/CodexProxy/commit/c9488f2b243b8ef7357a347488de2bcdeb3b3ff9) |
| Batch 2 | [e3e97ad9](https://github.com/router-for-me/CLIProxyAPI/commit/e3e97ad9be0e6be96be207574ba8ea3c8450dc81) | Bound namespace tool names and preserve identity through collisions | [c9488f2b](https://github.com/vpromise/CodexProxy/commit/c9488f2b243b8ef7357a347488de2bcdeb3b3ff9) |
| Batch 2 | [7def8425](https://github.com/router-for-me/CLIProxyAPI/commit/7def842554bc9a08f93e6317b61499e54eb20d6e) | Preserve function names on matching tool results | [c9488f2b](https://github.com/vpromise/CodexProxy/commit/c9488f2b243b8ef7357a347488de2bcdeb3b3ff9) |

These are adapted backports. Batch 1 retains terminal-event, cancellation, usage and explicit multi-agent opt-in behavior. Batch 2 retains tool/result pairing, explicit call IDs and media handling, while adding alternate reasoning fields and collision-safe names limited to 64 bytes.

The first attempt to publish only the two new patches exposed a dependency in `TestCappedToolNamesPreserveAdditionalToolsAndCustomRoundTrip`: the earlier orphan-output repair was required. Only that two-file repair was added as a separate prerequisite; the other v7.3.3 changes stayed local until the September 18 publication above. The failing composition was not published.

Final publication verification: full tests passed in **67 packages**, targeted race checks passed in **8 packages**, and the server build passed. Formatting and patch whitespace checks passed. Existing macOS exclusions were retained for `TestClaudeStandardRoundTripperBindsLocalIP` and `TestClaudeStandardRoundTripperHTTPSProxyKeepsBindAndProxyTLS` (127.0.0.2 binding limitation). GitHub main and its source tree were read back and matched the verified result. No real inference request was made.

## Earlier published groups

Each row maps a selected scope or a whole local batch to its source commits. A source listed here does not imply every file or policy from that upstream commit was imported. Where several fork commits appear, they identify the batch collectively rather than an unverified one-to-one mapping.

| Date / group | Upstream sources | Fork commits / scope |
| --- | --- | --- |
| 2026-09-07 / Claude baseline | Existing local adaptation | [57259d32](https://github.com/vpromise/CodexProxy/commit/57259d322998ceef72016b13f49c95a3f013d049) — Claude Code 2.1.258 compatibility |
| 2026-09-07 / protocol and reliability | [7bc16ee3](https://github.com/router-for-me/CLIProxyAPI/commit/7bc16ee3dbcfa108761efe57080c0fb43c475afb), [677dbe1d](https://github.com/router-for-me/CLIProxyAPI/commit/677dbe1dc5a5e0effd58d5c359e3fda6a9d3029f), [be1763e5](https://github.com/router-for-me/CLIProxyAPI/commit/be1763e59e2bd009aa9b343b7e84a2e5100e97d6), [07d81563](https://github.com/router-for-me/CLIProxyAPI/commit/07d8156375ca19ac977bb865d37fb77fa9e198ba), [6f25b9a1](https://github.com/router-for-me/CLIProxyAPI/commit/6f25b9a1495f8d2862a1c62e1bb5e36a6eb270cd), [c350d3f5](https://github.com/router-for-me/CLIProxyAPI/commit/c350d3f5205777e99470a266fa14b0ad6e5eb608), [6c6473f8](https://github.com/router-for-me/CLIProxyAPI/commit/6c6473f8998d3c8d3e79ea138bb431a3ca842027), [9fdc4605](https://github.com/router-for-me/CLIProxyAPI/commit/9fdc460585922ff38909e92dd3c7ac6dbe40419f), [893abbab](https://github.com/router-for-me/CLIProxyAPI/commit/893abbabc2a52a4494dbc8b42345d8dbf143abef), [6f161215](https://github.com/router-for-me/CLIProxyAPI/commit/6f16121554df25c9f9ab8de41e7b8157ef2d3c35), [cdda333c](https://github.com/router-for-me/CLIProxyAPI/commit/cdda333cd28703dce9cd31d5a57c9291c3171b86), [f804fb5f](https://github.com/router-for-me/CLIProxyAPI/commit/f804fb5f3077be299874d2d895de7ffdcdec880f), [ba2cdea3](https://github.com/router-for-me/CLIProxyAPI/commit/ba2cdea3b919a11b21b2db95e19a74295125c3d9), [5208aec7](https://github.com/router-for-me/CLIProxyAPI/commit/5208aec703b5ce7e3445f6e9d91cc13b3e78003a), [084f25c7](https://github.com/router-for-me/CLIProxyAPI/commit/084f25c798032a4eb3c86fc5b8599b4676773bf2) | [724bb58a](https://github.com/vpromise/CodexProxy/commit/724bb58adfc79b637c3b4513cbc6e73940ce4efe) — selected compatible fixes |
| Present before the 2026-09-07 batch | [7c2f6ce0](https://github.com/router-for-me/CLIProxyAPI/commit/7c2f6ce0d1a01c16a91dde727cb5a0fc7f226057), [8564142f](https://github.com/router-for-me/CLIProxyAPI/commit/8564142fb0ec94841c5d3fb76b28063786f2c7ca), [d2f71220](https://github.com/router-for-me/CLIProxyAPI/commit/d2f7122067b987958b0496b4ae4bdae641b69440) | Already covered at that review; no new import was needed for these entries |
| 2026-09-07 / quota and persistence | [18e01a76](https://github.com/router-for-me/CLIProxyAPI/commit/18e01a76ac72ba6597edd4786578d05ea8bcfb8a), [09471dd9](https://github.com/router-for-me/CLIProxyAPI/commit/09471dd9daba6691e5810e18012e973407260358), [5ab0bca0](https://github.com/router-for-me/CLIProxyAPI/commit/5ab0bca040cdfe17997c6f34da4247fa99518093), [1c22598d](https://github.com/router-for-me/CLIProxyAPI/commit/1c22598d0b5a1e2f9165ce451ee8add6b1964b83), [3db0a86c](https://github.com/router-for-me/CLIProxyAPI/commit/3db0a86c601543fb0e66b966046bd3cf6b542785) | [30f65ca8](https://github.com/vpromise/CodexProxy/commit/30f65ca838ecaa19b81191c4c045e32c1df449de) — adapted to local cooldown/persistence behavior |
| 2026-09-10 / Claude, Codex and auth reliability | [280b96ac](https://github.com/router-for-me/CLIProxyAPI/commit/280b96acead04d72433e80105b9818259f9b5b6b), [35a47238](https://github.com/router-for-me/CLIProxyAPI/commit/35a4723872a4a04aef053ba290eb362ccdf14d07), [ef99119e](https://github.com/router-for-me/CLIProxyAPI/commit/ef99119e57f4607893c6592827d79c69acd2792a), [e365ab0c](https://github.com/router-for-me/CLIProxyAPI/commit/e365ab0cc9882f992333d9e4b259014e8560bb25), [6e1f9ec4](https://github.com/router-for-me/CLIProxyAPI/commit/6e1f9ec4e0b6380c5bf531abc98da9188e10127a), [a59b1764](https://github.com/router-for-me/CLIProxyAPI/commit/a59b1764e781e0c20bf102349cd9bc202ce8f668), [82f4f370](https://github.com/router-for-me/CLIProxyAPI/commit/82f4f370df45f7a5a4be52f8101d4294f656ff02), [8696585c](https://github.com/router-for-me/CLIProxyAPI/commit/8696585cea4142c22b9ed4828d86dd1d8f04ba41), [03a054e3](https://github.com/router-for-me/CLIProxyAPI/commit/03a054e32fc1377edf32600fd6a6e863e25f30ae), [bee20b99](https://github.com/router-for-me/CLIProxyAPI/commit/bee20b9940251db2637597bc3944165d332ab097), [ba7e5583](https://github.com/router-for-me/CLIProxyAPI/commit/ba7e55836dee959e93ec6d41395865d9ec535086), [48e5e9e0](https://github.com/router-for-me/CLIProxyAPI/commit/48e5e9e03d219903646327feee015ac56ec25696), [1d5f7b2a](https://github.com/router-for-me/CLIProxyAPI/commit/1d5f7b2ac36f3737e28755c44ec825f4fc3a4800) | [cf2b6bc9](https://github.com/vpromise/CodexProxy/commit/cf2b6bc9165b5e21d9b880202b937bd807e2c12c), [3b3a41ec](https://github.com/vpromise/CodexProxy/commit/3b3a41eccc24c98bb7902a7d5e336457f1ff9e1e), [f9e5eaf0](https://github.com/vpromise/CodexProxy/commit/f9e5eaf0cbd9618b933dedcb8e02d4575decf868) — three selected batches, reviewed through [d1a024e9](https://github.com/router-for-me/CLIProxyAPI/commit/d1a024e9400bc65bd78ccd908945cf2eacc2835e) |
| 2026-09-14 / protocol | [75ce6352](https://github.com/router-for-me/CLIProxyAPI/commit/75ce6352939fe9b7b5b5c52efe34d330bd078868), [e696ea47](https://github.com/router-for-me/CLIProxyAPI/commit/e696ea47c5ee60085a30733649e0f629528097dd), [8c5f6e18](https://github.com/router-for-me/CLIProxyAPI/commit/8c5f6e185f4d58f9d475ab708a1752fcda9f239f), [638ed7e1](https://github.com/router-for-me/CLIProxyAPI/commit/638ed7e1cc5dc5d82cef40fe6e51f3b316dbab62), [c8ecb4f3](https://github.com/router-for-me/CLIProxyAPI/commit/c8ecb4f3c972664aae802e21c6a6743d5f6bd80a), [4edf9d1d](https://github.com/router-for-me/CLIProxyAPI/commit/4edf9d1dd6e303da0c932b099ef8c64830bb8fbe) | [47c201d6](https://github.com/vpromise/CodexProxy/commit/47c201d6733d3db3333dd121beb074b1ea371a27) — Claude CAQS v4; Codex turn-state headers; tool choice; split-CRLF SSE; reasoning ordering; usage effort |
| 2026-09-14 / claude-cache | [6a73f396](https://github.com/router-for-me/CLIProxyAPI/commit/6a73f39627358ecf3724fee2952fd234ada919b1), [377c315f](https://github.com/router-for-me/CLIProxyAPI/commit/377c315fd71f94158d957c99d083b08da7fbca51) | [47c201d6](https://github.com/vpromise/CodexProxy/commit/47c201d6733d3db3333dd121beb074b1ea371a27) — Claude initial-turn cache/billing continuity and explicit subagent one-hour TTL |
| 2026-09-14 / codex-transport | [bd03aabc](https://github.com/router-for-me/CLIProxyAPI/commit/bd03aabcf157e215ef50c5984d873f690a5c22bf), [3ae9093d](https://github.com/router-for-me/CLIProxyAPI/commit/3ae9093da837c32667d2f2b65e8ad448fdf1bb8f), [b5ba02c2](https://github.com/router-for-me/CLIProxyAPI/commit/b5ba02c2e3627e975538e217575abee9f73eccd8), [f702bc1a](https://github.com/router-for-me/CLIProxyAPI/commit/f702bc1ac2631953b3eea2d99eb6761b283a99ef) | [47c201d6](https://github.com/vpromise/CodexProxy/commit/47c201d6733d3db3333dd121beb074b1ea371a27) — Prewarm input; capacity bootstrap failure; upstream WebSocket pong handling; native Responses Lite adaptation |
| 2026-09-14 / codex-metadata | [8461b4e9](https://github.com/router-for-me/CLIProxyAPI/commit/8461b4e91d6f12a1cee02e7833917374db0a19bc) | [47c201d6](https://github.com/vpromise/CodexProxy/commit/47c201d6733d3db3333dd121beb074b1ea371a27) — Codex 0.154.0 client metadata |
| 2026-09-14 / cloudflare | [9fad5055](https://github.com/router-for-me/CLIProxyAPI/commit/9fad50550517501f4ae0967b56c1490c407afcba) | [47c201d6](https://github.com/vpromise/CodexProxy/commit/47c201d6733d3db3333dd121beb074b1ea371a27) — Cloudflare 520–526 transient classification |
| 2026-09-14 / local incident fixes | Local changes | [47c201d6](https://github.com/vpromise/CodexProxy/commit/47c201d6733d3db3333dd121beb074b1ea371a27) — confirmed quota error classification and atomic config replacement reloads; narrow known-Claude-beta preservation is included in this local compatibility group |
| 2026-09-14 / credential lifecycle | [12b88f3a](https://github.com/router-for-me/CLIProxyAPI/commit/12b88f3ad61469d4ea0e715c7f8ee4e4f12fb41d), [9812b1e7](https://github.com/router-for-me/CLIProxyAPI/commit/9812b1e76872d8201a44c8d32a2dde7c7e915b77), [4c1bebe8](https://github.com/router-for-me/CLIProxyAPI/commit/4c1bebe837a6e624215a97dddee5db11f0eeb8bf), [d1702fdf](https://github.com/router-for-me/CLIProxyAPI/commit/d1702fdffd2bb6903f985c60688ecf63f3b18ca2), [456d4c37](https://github.com/router-for-me/CLIProxyAPI/commit/456d4c371b51f0c0928cb5b2475051717c046746) | [102f79aa](https://github.com/vpromise/CodexProxy/commit/102f79aa99d32d3a0fe97563d56863f0749b65ac) — registration epochs, refresh/preparation merging, persistence order, watcher revisions and selective 401 recovery |
| 2026-09-14 / lifecycle review follow-up | Local changes | [581aa6b9](https://github.com/vpromise/CodexProxy/commit/581aa6b9f592efce9eb2c23c5c057492a0f45164) — preserve durable updates, restore model discovery after cooldown and keep quota sibling visibility correct |
| 2026-09-14 / narrow reliability follow-up | [02c02cda](https://github.com/router-for-me/CLIProxyAPI/commit/02c02cda50b71251d3a10e381e0c462453045d0b) | [b60d69b1](https://github.com/vpromise/CodexProxy/commit/b60d69b1d3151a9d282772e6c3c448821e921750) — listener shutdown, header snapshots and token-overflow checks only; excludes wsrelay/Home/plugin/BodySource changes |

The lifecycle integration keeps the approved five-day Codex refresh lead. Credential filenames were not migrated. The native Responses Lite integration retains a conditional session-header/affinity question as a separate deferred item; it must not be marked fully equivalent to every upstream header path.

## Deferred, conditional or excluded

The 2026-09-17 reassessment recommends **no additional batch now**. Revisit a candidate when it addresses a demonstrated problem in an active supported path and can be implemented with a narrow scope.

| Sources | Disposition and trigger |
| --- | --- |
| [2a6b87ac](https://github.com/router-for-me/CLIProxyAPI/commit/2a6b87aca083a5bf498ac1f68a1b636c500d7aaa) | Defer downstream WebSocket keepalive until an actual idle-disconnect path is identified |
| [1ca975df](https://github.com/router-for-me/CLIProxyAPI/commit/1ca975dfc011c320fd045a7ce9070d66e418fa22) | Do not add management cooldown snapshots now; diagnostic API/UI expansion without a demonstrated need |
| [8f23ad02](https://github.com/router-for-me/CLIProxyAPI/commit/8f23ad0291443c761b6748bb6068ca32f9dca2c1), [294b7f5b](https://github.com/router-for-me/CLIProxyAPI/commit/294b7f5b191bb9312d5ed3d4652b2db0279afbf5), [4311ae87](https://github.com/router-for-me/CLIProxyAPI/commit/4311ae874774014a6cfa63a251e2f989ed189bad), [678da561](https://github.com/router-for-me/CLIProxyAPI/commit/678da56193fbea407b6dc57321e3f2a5cd290f68) | Alias-template lookup is a conditional narrow repair when active aliases/prefixes need it; do not bundle capability policy, search advertisements or catalog replacement |
| [6724a958](https://github.com/router-for-me/CLIProxyAPI/commit/6724a95851f912c365846736de75db5a38278f95) | Do not change scheduling based on the earlier direct-helper fixture: all nine manager-level checks preserved recovery hints for the temporary/disabled pool. Other cases need a manager/execution-path reproduction |
| [e3cbe437](https://github.com/router-for-me/CLIProxyAPI/commit/e3cbe437d00b36fd41a00a61243fc2625ca96b57) | Defer broad lifecycle lock/async restructuring until actual contention is demonstrated |
| [bef1f65c](https://github.com/router-for-me/CLIProxyAPI/commit/bef1f65c6c1daede091850aa96d2e1513f5e883c) / [PR #5804](https://github.com/router-for-me/CLIProxyAPI/pull/5804) | Do not copy broad transport retries. A setup-only alternative still needs a demonstrated failure and an agreed exhaustion/cooldown contract; PR #5804 was unmerged at the recorded review |
| [4cd17293](https://github.com/router-for-me/CLIProxyAPI/commit/4cd17293a3fd8ec52b396632fc68439cf2f47e48), [c4982e84](https://github.com/router-for-me/CLIProxyAPI/commit/c4982e846e16c4f6b7a5ffd7cf719dd29106fac8) | Tool-result image relay is conditional on a used OpenAI-compatible backend rejecting the present shape; assess image placement and text-only filtering together |
| [b908ed5d](https://github.com/router-for-me/CLIProxyAPI/commit/b908ed5d8644fe63fe0ffcfd6c2e14ca6b919b99) | Git-store recovery is conditional on active Git-backed credential storage; deployed storage was not inspected |
| [fc96a87f](https://github.com/router-for-me/CLIProxyAPI/commit/fc96a87fa66c0cfd8b4746449a178955229d9db8) | Credential filename migration remains deferred |
| Feature families / other policies | Keep Merkle/LCP, distributed Home, plugin expansion, arbitrary Claude beta forwarding, lossy tool-schema rewriting and unrelated model-catalog changes deferred |
| Unsupported native providers | Do not reintroduce Gemini, Interactions, Vertex, AI Studio, Antigravity, Kimi, xAI/Grok or other out-of-scope native executors; shared supported-path fixes may still be reviewed separately |

`prefer_websockets: true` on an OpenAI-compatible model is not automatically a local defect: the fork can accept downstream WebSockets and execute upstream through HTTP. A test expecting upstream's policy is not sufficient evidence to change this behavior.

## How to continue the next review

1. Check the current branch, working tree and remote. All selected fixes listed above were published at this checkpoint; inspect any later local changes separately.
2. Compare newly fetched upstream commits after `8c664b2fede5c83b919be1df9b01057ec4e4c950`. Older deferred items above remain deferred unless their trigger is met.
3. Search this file for a source SHA and inspect the associated fork commit before importing anything again. Verify behavior, not only ancestry or commit titles.
4. Record exact selected scope, prerequisite commits, preserved local policies and verification results. Changes to cooldown, disable or scheduling behavior require the previously requested discussion before implementation.
5. After publication, update this ledger with the actual fork commits and source tree read back from GitHub. Record deployment separately.

## Local evidence archive

The essential publication mapping is included above so this file remains useful in a fresh clone. Detailed patch, hash and test records also exist beside the local repository, under the following directories; they are audit records, not build caches:

- `upstream-sync-2026-09-07/` and `upstream-sync-2026-09-10/`
- `upstream-sync-2026-09-14/`, `auth-lifecycle-2026-09-14/` and `reliability-followup-2026-09-14/`
- `upstream-sync-v7.3.3-2026-09-15/`
- `upstream-sync-v7.3.6-batch1-2026-09-17/` and `upstream-sync-v7.3.6-batch2-2026-09-17/`
- `publication-v7.3.6-2026-09-17/` — independent publication verification and remote readback
- `publication-v7.3.3-remaining-2026-09-18/` — remaining repairs, exact source-tree comparison and publication readback
- `upstream-minimal-scope-review-2026-09-17/` — corrected manager-level probes and current deferrals
