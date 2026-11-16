# 独立 Console 节点适配

此分支为 CodexProxyConsole 增加可选、独立私网 TLS1.3 mTLS 节点接口。默认关闭，不替换原管理登录、单节点自助邀请或推理端口。完整控制台项目位于同级 `CodexProxyConsole`，协议事实来源为该项目 `api/node/v1/README.md`；双方共享能力 fixture。

新配置为 `console-node`：enabled、稳定 node-id、安装实例 instance-id、私网 listen、节点 cert/key、Console client-ca 和 leaf SHA256、统一 public-origin、版本化 egress-profiles。请使用控制台提供的离线证书 CLI 和配置示例。只支持原生 Claude/Codex、文件凭据及已有隔离号池，不自动开启全局隔离或改动原账号权限。

纳管流程保存不可变 owner/profile/version/ProxyURL。原生 OAuth start/complete、完整凭据保存与 disabled 导入复用既有实现。节点返回元数据，provider token/PKCE 永不返回 Console；单池 inference key 仅用于可选 new-api 普通渠道。所有控制动作均需证书及安装身份匹配。

`direct-ip://<local IPv4>` 为显式源地址路由；共享 HTTP/原生 OAuth/uTLS 及 Codex WS 拨号均支持，IPv6/缺失本地地址不能回退。运行、续期、pool 选择和管理 APICall 核验绑定，管理 APICall 不允许为纳管凭据覆盖出口。配置/目录变更让控制接口停止写操作，重启后仍需原 owner/出口匹配；不要使用这一流程自动迁移或恢复旧 refresh token。

审批按已有同 provider 池追加成员，保持 disabled；启用须预期账号 revision、有效凭据及兼容出口。配置读取采用同一发布快照，修复此前模型注册与配置重载的读取竞态。历史 accepted 与当前库存分开，磁盘仍有文件但运行未载入显示 unavailable，删除后的旧任务不复活账号。

重新授权从原始 accepted 记录创建，要求先明确禁用、固定原节点/出口和预期 revision。审核时主体及组织必须相同，只更新凭据材料，保留池身份、设备、Thread、人工配置和禁用状态；最新版本发生变化时拒绝覆盖。重新授权成功后仍需显式启用，不创建第二个账号。

操作先持久化 uncertain 回执，变更后写安全结果；同 ID 改参数拒绝。邀请链接只创建时显示；丢回包先查询/撤销，再新建，不从摘要恢复 bearer。不确定单池 key 操作只能读取仍存在的原 key，不能重建管理员删掉的 key。日志/回执不含 callback、code、refresh token、PKCE 或原始邀请 bearer。

验证覆盖节点原生授权闭环、审批/启用/过期/重新授权、换主体拒绝、过时 revision、mTLS、配置变更、能力契约、HTTP/WS 固定源地址与失败拒绝、删除及不确定回执。CodexProxy 全量测试、相关竞态与编译和 Console 的独立验证见同级项目 `docs/validation.zh-CN.md`。

本分支只完成本地代码与离线模拟验证，未推送/合并 main、未部署、未进行真实 provider exchange/refresh/inference，也未改生产 new-api。实际公网 E、浏览器登录 IP、真实客户端两级亲和与流/续轮须按控制台部署说明分层验收。关闭适配后纳管账号会停止通过新 guard；回滚须保留当前凭据并明确管理绑定，不能还原旧 token。
