# PRD: Resin US 优质出口池运维（不改 grok2api 源码）

## Goal / User value

为 grok2api 提供 **专用、仅 US、质量筛选过的 Resin 出口池**，并在 **不修改、不重编译 grok2api 源码** 的前提下，尽量降低：

1. 正常会话中无意义换 IP 带来的上游风控风险
2. 坏节点/假活出口长期残留导致账号连续踩雷
3. 后续升级 grok2api 官方版本时的本地补丁冲突

用户价值：出口更稳、可运维刷新、升级友好。

## Confirmed facts（仓库/环境已核实）

### 已落地

- Resin 平台名：`grok2api`
- 约束：`region_filters: ["us"]`，质量入池门槛 score ≥ 90
- 分配策略：`PREFER_LOW_LATENCY`，`sticky_ttl: 24h`
- 入池方式：`regex_filters` 锚定筛选后的 node tag；非运行时按历史质量分全局 argmax
- grok2api 已配置 4 个 egress 节点（build/web/console/web_asset），代理模板：
  - `socks5h://grok2api.{account}:<RESIN_PROXY_TOKEN>@resin:2260`
- grok2api 容器已接入 `resin_default` 网络；`docker-compose.yml` 已写 external network
- 旧 warp 出口保持 disabled
- 维护脚本：`/root/resin/setup_grok2api_platform.py`（默认 `--regions us`）
- 质量工具：`/root/resin/resin_quality_check.py`

### 行为事实

- **在线换 IP ≠ 每次质量最高**：先入优质池，再在池内 P2C + 低延迟/负载选择
- **节点真挂/熔断**：Resin 会自动换（先同 IP，再新 IP）
- **假活（能连但业务/CF 风控）**：当前 **不会** 在 grok2api 请求内智能换 IP（因不改 grok2api 源码）
- 质量分是 **刷新时刻快照**，权重偏可用性/延迟；对 grok 业务上游覆盖不完整（曾用 `grok.com` 首页，target 分类器偏 ChatGPT 特征）

### 硬约束（产品决策已表达）

- **不触碰 grok2api 源代码**（避免后续更新冲突）
- 完整“请求内风控分类 + account generation 换粘性”不在本任务范围
- 优先 **运维/脚本/Resin 配置/Admin API 配置** 路径

## Requirements（当前理解）

### R1. 平台与范围
- 保持/强化 Resin 专用平台 `grok2api`，仅 US 节点
- 目标可路由优质节点约 **60**（55–65 浮动）
- grok2api 继续通过现有 egress 配置使用该平台（不改源码）

### R2. 质量入池（需补强）
- 定期/可手动刷新优质节点集
- 入池标准应 **更贴近 grok2api 真实上游**，而不仅是通用连通 + grok.com 首页
- 刷新应 **增量优先**：尽量保留仍健康的旧节点，避免全量洗牌打散粘性
- 被质量守护隔离的出口 IP 持久降低信誉权重，主池和即时轮换均优先选择高权重 IP
- 即时轮换只替换目标 Resin 分片中命中被隔离 IP 的标签，不改动同分片其他健康 IP

### R3. 坏节点处理（无源码近似任务2）
- 检测失败/变差的节点应在 **约 15 分钟**内移出平台
- 巡检间隔目标 ≤ 10–15 分钟
- 移出后依赖 Resin 既有粘性失效/重绑逻辑换出口
- 被移出 IP/节点脚本级冷藏 **2 小时**，避免立刻加回；期满后须复检合格才可回池

### R4. 粘性策略
- sticky 需在「少换 IP」与「坏 IP 别粘太久」之间折中
- **确认 sticky_ttl = 24h**

### R5. 可运维
- 一键/定时刷新脚本
- 产出可审计报告（入选/淘汰/冷藏、分数、原因）
- 不要求改 resin-src（除非后续单独立项）

## Acceptance criteria

1. 不修改 `/root/grok2api` 业务源码（允许 Trellis 任务文档、运维说明；不允许为功能改 Go/前端源码）。
2. Resin 平台 `grok2api` 始终 `region_filters` 含且仅面向 US 使用策略（us only）。
3. 存在可重复执行的刷新流程：质量检测 → 增量更新平台节点 → 写出报告。
4. 刷新默认尽量保留仍合格的旧节点；全量重建只能显式开启。
5. 文档明确三类行为：
   - 节点挂了：Resin 自动换
   - 假活风控：依赖检测周期淘汰，非请求内瞬时逃生
   - 在线选路：池内 P2C/低延迟，非质量分第一名必中
6. 质量探测必须覆盖 `cli-chat-proxy.grok.com` 与 `grok.com`，且入池/淘汰规则可解释、可从报告审计。
7. grok2api 四个 scope 的 resin egress 在刷新后仍可用（probe 或等价验证）。

## Out of scope

- 修改 grok2api Go/前端源码实现请求内 generation / 账号级 quarantine
- 修改 resin-src 增加 ban-IP/drop-lease API（可后续任务）
- 住宅代理采购/更换订阅源商务问题
- 保证不被 grok/CF 风控（只能降低概率）
- 任务1若定义为“仅加长 sticky 到 24h”可并列，但不是本 PRD 必做除非确认
- 双平台逃生池与 grok2api Admin API 整池自动切换（已明确不做）
- 请求内账号级换 IP / 改 grok2api 源码的完整任务2

## Decisions

1. **坏 IP/节点最长残留时间：15 分钟**（用户确认）
   - 巡检周期目标：≤ 10–15 分钟
   - sticky 不应在“已知坏节点仍留在池内”时无限保护坏出口；健康节点仍可按 sticky 粘住
   - 验收：不合格节点应在约 15 分钟窗口内被移出平台可路由集（允许一次巡检抖动）

2. **质量探测目标集：A**（用户确认）
   - 主业务目标：`https://cli-chat-proxy.grok.com`（Build 主路径代表性）
   - Web/边缘目标：`https://grok.com`（CF/拦截特征）
   - 通用延迟：保留 gstatic generate_204（或等价）
   - 不做硬淘汰：`accounts.x.ai` / `auth.x.ai`
   - 本阶段不加必选：`api.x.ai`（可作为后续加分项）

3. **sticky_ttl：24h**（用户确认，保持当前）
   - 健康账号继续粘出口 IP 约 24 小时（租约固定不续期）
   - 坏节点主要靠 ≤15 分钟巡检移出可路由集后触发重绑，而不是靠缩短 sticky

4. **目标池大小：约 60 个 US 节点**（用户后续调整）
   - 允许浮动约 55–65（不足则补，过多则按质量分与 IP 权重淘汰垫底）
   - 服务于 15 分钟全量双目标巡检可完成

5. **坏节点冷藏：2 小时**（用户确认）
   - 冷藏键优先 egress IP，其次 node tag
   - 冷藏期内禁止重新入选 `grok2api` 平台
   - 满 2h 后需再次通过双目标质量检测才能回池

6. **刷新调度：每 10 分钟自动执行**（用户确认，选项 A）
   - 本机 Resin 侧 systemd timer/cron 调用维护脚本
   - 支持失败日志；不改 grok2api 源码
   - 可另保留手动执行同一脚本的入口

7. **双逃生池：不做（MVP）**（用户确认，选项 A）
   - 不创建 `grok2api-escape`，不做 Admin API 整池自动切换
   - 依赖单池 10 分钟增量巡检 + 2h 冷藏 + Resin 熔断重绑

## Open questions

（规划阶段关键问题已关闭；实现中若遇探测路径/API 细节可再补。）


1. ~~坏 IP 最长残留时间~~ → **15 分钟**
2. ~~质量探测目标集~~ → **A：cli-chat-proxy + grok.com + 通用延迟**
3. ~~sticky_ttl~~ → **24h**（用户后续确认）
4. ~~双逃生池~~ → **不做（MVP）**
5. ~~刷新调度~~ → **每 10 分钟自动**（可附带手动入口）
6. ~~冷藏时长~~ → **2h**；再次入池须重新通过检测
7. ~~目标规模~~ → **约 60（55–65 浮动）**
8. ~~隔离 IP 的轮换与权重~~ → **仅替换被隔离 IP；一次隔离权重降低 25（100 起步，最低 10）**

## Notes from prior discussion

- 用户明确拒绝为任务2改 grok2api 源码
- 完整任务2与无源码近似版已区分
- 质量测量适合粗筛，需业务向补强
