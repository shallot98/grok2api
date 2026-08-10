# 替换为 chenyme/grok2api

## 目标

将当前工作目录中的业务项目从 `jiujiu532/grok2api` 替换为 `https://github.com/chenyme/grok2api`，最终代码、Git 历史和远端指向目标仓库，同时保证替换过程可审计、可回退。

## 已确认事实

- 当前业务仓库远端为 `jiujiu532/grok2api`，当前分支为 `main`。
- 当前工作区包含未纳入 Git 的项目管理文件：`.agents/`、`.claude/`、`.codex/`、`.trellis/`、`AGENTS.md`。
- 当前运行配置与数据包括被忽略的 `.env`、`data/`、`logs/` 和 `.venv/`。
- 目标仓库不是当前仓库的 Git 祖先或后代，两个仓库没有共同提交祖先，不能普通 fast-forward 替换。
- 目标仓库采用 `backend/`、`frontend/`、`config.yaml` 和 Docker named volume；当前仓库采用 `app/`、`.env`、`config.defaults.toml` 及宿主机 `data/`、`logs/` 挂载。
- 两个项目的数据模型和配置格式不同，当前 `.env` 与 `data/` 不能直接挂载到目标项目。
- 旧 `accounts.db` 有 606 个 Grok Web SSO 账号：304 个 active、91 个 cooling、211 个 expired，且均未软删除。
- 2026-07-14 对全部 606 个账号调用 Grok `rate-limits` 接口只读复检：334 个可用、95 个暂时限流、177 个明确返回无效凭据。
- 旧状态存在偏差：211 个 expired 中有 34 个当前仅返回 429，不能视为永久失效；91 个 cooling 中有 76 个已经恢复可用。
- 目标项目支持最多 10000 个 Grok Web SSO 账号的纯文本或 JSON 批量导入；旧 `accounts.token` 与目标 `sso_token` 语义一致，可通过导出—导入迁移。
- 旧数据库的额度、使用次数、冷却状态和历史统计字段与目标 schema 不同，不能直接迁移；目标项目导入后需自行重新同步账号状态。
- 旧媒体文件缺少目标数据库中的媒体元数据，不能直接作为完整媒体记录复用；仅作为备份文件保留。
- `.env` 不能整体复用；其中端口、代理等非敏感运行参数可人工映射，API Key 和管理员认证需在目标系统重新建立。

## 要求

- 替换前将旧代码、`.env`、`data/`、`logs/` 完整备份到仓库外的时间戳目录，建立明确可回退点。
- 保留 Trellis 与 Codex 项目管理文件，使替换后仍能继续当前任务。
- 将业务源码、Git 分支与远端切换到 `chenyme/grok2api` 当前 `main`。
- 按目标仓库要求生成或准备运行配置，不把密钥写入受版本控制的源码。
- 替换后验证仓库来源、工作区状态、配置完整性以及目标项目提供的构建或测试命令。

## 验收标准

- `origin` 指向 `https://github.com/chenyme/grok2api.git`。
- 当前 `HEAD` 与替换时获取的目标仓库 `origin/main` 一致，或仅包含明确记录的本地管理文件提交。
- 当前项目旧业务源码不再混留于目标源码树。
- `.trellis/`、`.agents/`、`.codex/`、`.claude/` 和 `AGENTS.md` 保留。
- 旧 `.env`、`data/`、`logs/` 已按用户选择迁移、归档或清除，结果明确可验证。
- 目标项目的官方构建或测试命令执行成功；若环境限制导致无法执行，必须明确报告 `没验证` 和实际错误。
- 替换前状态存在独立备份路径或 Git 引用，可用于恢复。

## 范围外

- 未经明确要求，不把旧项目账号、Token 或数据库结构自动转换为目标项目格式。
- 不修改目标项目业务功能。
- 不在替换过程中升级目标仓库之外的依赖或重构代码。

## 已确认决策

- 替换前将旧 `.env`、`data/`、`logs/` 完整备份到仓库外。
- 基于结构证据复用兼容数据；未经验证不直接复用数据库。
- 迁移时排除实时复检确认永久失效的 177 个账号，迁移其余 429 个账号。
- 自动生成管理员密码、JWT Secret 和凭据加密密钥；只写入仓库外权限为 `600` 的凭据文件及被 Git 忽略的 `config.yaml`。

## 待确认

- 无。

## 账号复检证据

- 报告文件：`account-validation.json`。
- 报告仅保存 Token 的 SHA-256，不保存或输出原始凭据。
- `permanently_invalid` 仅指 400/401/403 且响应包含 `invalid-credentials`、`bad-credentials`、`session not found`、`blocked-user`、`token revoked` 等明确标志。
- HTTP 429 归类为 `temporarily_rate_limited`，不会作为删除或排除依据。
